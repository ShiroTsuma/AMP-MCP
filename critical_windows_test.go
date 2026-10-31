//go:build windows

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/windows"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf16"
	"unsafe"
)

func fixtureToken(t *testing.T) string {
	t.Helper()
	u := utf16.Encode([]rune("synthetic-service-token"))
	raw := make([]byte, len(u)*2)
	for i, v := range u {
		raw[2*i] = byte(v)
		raw[2*i+1] = byte(v >> 8)
	}
	in := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var out windows.DataBlob
	if e := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); e != nil {
		t.Fatal(e)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return hex.EncodeToString(unsafe.Slice(out.Data, int(out.Size)))
}

func TestCriticalSessionRecoveryAndNoMutationReplay(t *testing.T) {
	var logins, reads, writes atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/API/Core/Login":
			logins.Add(1)
			w.Write([]byte(`{"success":true,"sessionID":"synthetic-session"}`))
		case "/API/Core/GetAPISpec":
			w.Write([]byte(`{"Core":{"GetStatus":{},"Start":{}}}`))
		case "/API/Core/GetStatus":
			if reads.Add(1) == 1 {
				w.Write([]byte(`{"Title":"Unauthorized Access","Message":"requires Session.Exists permission"}`))
			} else {
				w.Write([]byte(`{"State":20}`))
			}
		case "/API/Core/Start":
			writes.Add(1)
			w.Write([]byte(`{"Title":"Unauthorized Access","Message":"requires Session.Exists permission"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer ts.Close()
	c, e := newClient(Connection{URL: ts.URL, Username: "fixture", Token: fixtureToken(t)})
	if e != nil {
		t.Fatal(e)
	}
	c.http = ts.Client()
	if _, e = c.call(context.Background(), "", "Core/GetStatus", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if reads.Load() != 2 || logins.Load() != 2 {
		t.Fatalf("read/session counts %d/%d", reads.Load(), logins.Load())
	}
	if _, e = c.call(context.Background(), "", "Core/Start", map[string]any{}); !isSessionError(e) {
		t.Fatalf("expected session error, got %v", e)
	}
	if writes.Load() != 1 {
		t.Fatal("mutation was replayed")
	}
	if _, e = c.call(context.Background(), "", "Unknown/Method", map[string]any{}); e == nil {
		t.Fatal("unsupported endpoint accepted")
	}
}

func TestCriticalJobPersistenceAndUncertainOutcome(t *testing.T) {
	t.Setenv("AMP_MCP_CONFIG", filepath.Join(t.TempDir(), "connections.json"))
	conn := Connection{URL: "https://example.invalid", Username: "fixture"}
	j, e := beginJob(Definition{Name: "amp_delete_instance", Endpoint: "ADSModule/DeleteInstance"}, "fixture", conn, "", "00000000-0000-0000-0000-000000000001")
	if e != nil {
		t.Fatal(e)
	}
	v, e := finishSubmission(j, map[string]any{"Id": "00000000-0000-0000-0000-000000000002", "State": float64(0), "ProgressPercent": float64(12)}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if v == nil {
		t.Fatal("missing job result")
	}
	jobs, e := readJobs()
	if e != nil {
		t.Fatal(e)
	}
	saved := jobs[j.ID]
	if saved.State != "running" || saved.TaskID == "" {
		t.Fatalf("unexpected job %+v", saved)
	}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
	defer ts.Close()
	conn.URL = ts.URL
	c, _ := newClient(conn)
	c.http = ts.Client()
	c.sessions[""] = "fixture-session"
	c.capabilities[""] = map[string]any{"Core": map[string]any{"GetTasks": map[string]any{}}}
	saved.Target = targetFingerprint(conn)
	if e = saveJob(saved); e != nil {
		t.Fatal(e)
	}
	a := &App{clients: map[string]*Client{"fixture": c}}
	if e = writeJSONAtomic(configPath(), Config{Version: 1, Connections: map[string]Connection{"fixture": conn}}); e != nil {
		t.Fatal(e)
	}
	jobs, _ = readJobs()
	saved = jobs[j.ID]
	got, e := a.refreshJob(context.Background(), saved)
	if e != nil {
		t.Fatal(e)
	}
	if got.State != "unknown" {
		t.Fatal("missing task was incorrectly treated as success")
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "fixture-session") {
		t.Fatal("session leaked into job")
	}
	// Retain Finished evidence after task dismissal; neither a future unknown
	// state nor 100% progress alone establishes success.
	finished := 4
	jobs, e = readJobs()
	if e != nil {
		t.Fatal(e)
	}
	got = jobs[j.ID]
	got.RemoteState = &finished
	if e = saveJob(got); e != nil {
		t.Fatal(e)
	}
	jobs, _ = readJobs()
	got, e = a.refreshJob(context.Background(), jobs[j.ID])
	if e != nil || got.State != "succeeded" || taskStateName(finished) != "Finished" {
		t.Fatalf("persisted Finished task was not reconciled: %+v, %v", got, e)
	}
	for _, tt := range []struct {
		code  int
		state string
	}{{0, "running"}, {1, "submitted"}, {2, "submitted"}, {3, "failed"}, {4, "succeeded"}, {5, "running"}, {6, "failed"}, {7, "unknown"}, {100, "unknown"}, {254, "failed"}, {12345, "unknown"}} {
		observed := Job{}
		observeTask(&observed, map[string]any{"State": float64(tt.code), "ProgressPercent": float64(100)})
		if observed.State != tt.state {
			t.Fatalf("task %d: got %s, want %s", tt.code, observed.State, tt.state)
		}
	}
}

func TestCriticalSecretRedactionAndTextChunks(t *testing.T) {
	v := map[string]any{"Node": "Core.Security.ApiKey", "InputType": "text", "CurrentValue": "synthetic-secret", "DefaultValue": "synthetic-default", "EnumValues": map[string]any{"x": "synthetic-secret"}}
	redact(v)
	if v["CurrentValue"] != nil || v["DefaultValue"] != nil || v["EnumValues"] != nil {
		t.Fatal("secret setting value escaped redaction")
	}
	metadata, e := safeSettingsMetadata(map[string]any{"Core": []any{map[string]any{"Node": "Core.Public.Setting", "CurrentValue": "must-not-appear", "Name": "Setting"}}}, map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(metadata)
	if strings.Contains(string(b), "must-not-appear") {
		t.Fatal("metadata leaked setting value")
	}
	chunk, e := readTextFileResult(map[string]any{"Result": "aGVsbG8="}, 0, 64)
	if e != nil || chunk.(map[string]any)["text"] != "hello" {
		t.Fatal("file chunk decoding failed")
	}
	if _, e = normalizeRemote("../secret"); e == nil {
		t.Fatal("parent traversal accepted")
	}
	text, count, start, partial, e := tailText([]byte("partial\r\none\r\ntwo\r\n"), 100, 1)
	if e != nil || text != "two\n" || count != 1 || start <= 100 || partial {
		t.Fatal("bounded tail returned historical/partial lines")
	}
	if _, _, _, _, e = tailText([]byte{0xff}, 0, 10); e == nil {
		t.Fatal("invalid UTF-8 tail accepted")
	}
	view, _ := json.Marshal(jobView(Job{ID: "synthetic", Target: "internal-binding", Instance: "same", Scope: "same"}))
	if strings.Contains(string(view), "internal-binding") || strings.Contains(string(view), `"scope"`) {
		t.Fatal("internal/redundant job binding leaked into public output")
	}
	filtered, e := safeSettingsMetadata(map[string]any{"Core": []any{map[string]any{"Node": "Core.Public.Setting", "Name": "Setting", "CurrentValue": "must-not-appear"}}}, map[string]any{"query": "must-not-appear"})
	if e != nil || len(filtered.(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("metadata search matched a secret/current value")
	}
}
