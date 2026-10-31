//go:build windows

package main

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fixtureRoundTripper func(*http.Request) (*http.Response, error)

func (f fixtureRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Security boundary test, not a suite mirroring individual AMP endpoints.
func TestCriticalDelegationDPAPIAndDownloadIntegrity(t *testing.T) {
	cipher, e := encryptCredential("synthetic-password-Aa1!")
	if e != nil {
		t.Fatal(e)
	}
	plain, e := decryptToken(cipher)
	if e != nil || plain != "synthetic-password-Aa1!" {
		t.Fatal("DPAPI round trip failed")
	}
	scope := "00000000-0000-0000-0000-000000000001"
	rootLogins, delegations, childLogins := 0, 0, 0
	selfReads := 0
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/API/Core/Login":
			rootLogins++
			if body["token"] != "synthetic-password-Aa1!" {
				t.Error("incorrect ADS credential")
			}
			w.Write([]byte(`{"success":true,"sessionID":"root-session"}`))
		case "/API/ADSModule/ManageInstance":
			delegations++
			if r.Header.Get("Authorization") != "Bearer root-session" || body["InstanceId"] != scope {
				t.Error("delegation routing failed")
			}
			w.Write([]byte(`{"Status":true,"Result":"delegated-only"}`))
		case "/API/Core/GetModuleInfo":
			w.Write([]byte(`{"InstanceId":"00000000-0000-0000-0000-000000000099"}`))
		case "/API/ADSModule/Servers/00000000-0000-0000-0000-000000000099/API/Core/GetAPISpec":
			if r.Header.Get("Authorization") != "Bearer root-session" {
				t.Error("self scope must reuse ADS session")
			}
			w.Write([]byte(`{"Core":{"GetStatus":{}}}`))
		case "/API/ADSModule/Servers/00000000-0000-0000-0000-000000000099/API/Core/GetStatus":
			selfReads++
			if selfReads == 1 {
				w.Write([]byte(`{"Title":"Unauthorized Access","Message":"requires Session.Exists permission"}`))
			} else {
				w.Write([]byte(`{"State":20}`))
			}
		case "/API/ADSModule/Servers/" + scope + "/API/Core/Login":
			childLogins++
			if body["token"] != "delegated-only" {
				t.Error("service credential leaked to child")
			}
			w.Write([]byte(`{"success":true,"sessionID":"child-session"}`))
		case "/API/ADSModule/Servers/" + scope + "/API/Core/GetAPISpec":
			if r.Header.Get("Authorization") != "Bearer child-session" {
				t.Error("child session routing failed")
			}
			w.Write([]byte(`{"Core":{}}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer ts.Close()
	c, e := newClient(Connection{URL: ts.URL, Username: "fixture", Token: cipher})
	if e != nil {
		t.Fatal(e)
	}
	c.http = ts.Client()
	if _, e = c.call(context.Background(), scope, "Core/GetAPISpec", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if rootLogins != 1 || delegations != 1 || childLogins != 1 {
		t.Fatal("unexpected authentication replay")
	}
	if _, e = c.call(context.Background(), "00000000-0000-0000-0000-000000000099", "Core/GetStatus", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if rootLogins != 2 || delegations != 1 || selfReads != 2 {
		t.Fatal("ADS self-scope refresh or routing failed")
	}
	// Native instance tokens must never be sent to ADS Login/ManageInstance.
	native, e := newClient(Connection{URL: ts.URL, Username: "native", Token: cipher, InstanceID: scope})
	if e != nil {
		t.Fatal(e)
	}
	native.http = ts.Client()
	nativeLogins := 0
	native.http = &http.Client{Transport: fixtureRoundTripper(func(r *http.Request) (*http.Response, error) {
		prefix := "/API/ADSModule/Servers/" + scope + "/API/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Errorf("native credential escaped instance: %s", r.URL.Path)
		}
		endpoint := strings.TrimPrefix(r.URL.Path, prefix)
		body := `{}`
		switch endpoint {
		case "Core/Login":
			nativeLogins++
			var args map[string]any
			json.NewDecoder(r.Body).Decode(&args)
			if args["username"] == "native-password" {
				if args["password"] != "synthetic-password-Aa1!" || args["token"] != "" {
					t.Error("native password used the wrong authentication field")
				}
			} else if args["token"] != "synthetic-password-Aa1!" || args["password"] != "" {
				t.Error("native token used the wrong authentication field")
			}
			body = `{"success":true,"sessionID":"native-session"}`
		case "Core/GetAPISpec":
			body = `{"Core":{"GetStatus":{}}}`
		case "Core/GetStatus":
			body = `{"State":20}`
		default:
			t.Errorf("unexpected native endpoint: %s", endpoint)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	if _, e = native.call(context.Background(), scope, "Core/GetStatus", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if _, e = native.call(context.Background(), "", "Core/GetStatus", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if nativeLogins != 1 {
		t.Fatal("native scope aliases did not reuse the native session")
	}
	if _, e = native.call(context.Background(), "00000000-0000-0000-0000-000000000088", "Core/GetStatus", map[string]any{}); e == nil {
		t.Fatal("native connection escaped its instance")
	}
	if _, e = native.apiSpec(context.Background(), scope, true); e != nil {
		t.Fatal(e)
	}
	pw, e := newClient(Connection{URL: ts.URL, Username: "native-password", Password: cipher, InstanceID: scope})
	if e != nil {
		t.Fatal(e)
	}
	pw.http = native.http
	if _, e = pw.call(context.Background(), scope, "Core/GetStatus", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if nativeLogins != 2 {
		t.Fatal("native password login was not verified")
	}
	if _, e = newClient(Connection{URL: ts.URL, Password: cipher}); e == nil {
		t.Fatal("unbound password authentication accepted")
	}

	// Replace only the transport, so no download can reach the network.
	saved := http.DefaultTransport
	defer func() { http.DefaultTransport = saved }()
	data := "synthetic binary\x00jar"
	sum := sha512.Sum512([]byte(data))
	hash := hex.EncodeToString(sum[:])
	http.DefaultTransport = fixtureRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
	})
	got, e := fetchInstallFile(context.Background(), "https://example.invalid/release.jar", hash, 100)
	if e != nil || string(got) != data {
		t.Fatal("verified download failed")
	}
	for _, x := range []struct {
		url, hash string
		limit     int64
	}{
		{"https://example.invalid/release.jar", strings.Repeat("0", 128), 100},
		{"https://example.invalid/release.jar", hash, 3},
		{"http://example.invalid/release.jar", hash, 100},
		{"https://user:password@example.invalid/release.jar", hash, 100},
	} {
		if _, e = fetchInstallFile(context.Background(), x.url, x.hash, x.limit); e == nil {
			t.Fatal("unsafe or unverifiable download accepted")
		}
	}
}
