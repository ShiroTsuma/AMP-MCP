package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"amp-mcp/internal/panel"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func init() { panel.OpenBrowser = func(string) error { return nil } } // never launch a real browser

// tempStore isolates config and secrets; the Linux vault unlocks from the env.
func tempStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AMP_MCP_CONFIG", filepath.Join(dir, "connections.json"))
	t.Setenv("MCP_SECRETS_PASSPHRASE", "test-passphrase")
	return dir
}

// fakeAMP records the credentials each login used.
func fakeAMP(t *testing.T) (*httptest.Server, string, func() []map[string]any) {
	var mu sync.Mutex
	var logins []map[string]any
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/API/Core/Login"):
			mu.Lock()
			logins = append(logins, body)
			mu.Unlock()
			w.Write([]byte(`{"success":true,"sessionID":"synthetic-session"}`))
		case strings.HasSuffix(r.URL.Path, "/API/Core/GetAPISpec"):
			w.Write([]byte(`{"Core":{"GetModuleInfo":{}}}`))
		case strings.HasSuffix(r.URL.Path, "/API/Core/GetModuleInfo"):
			w.Write([]byte(`{"AMPVersion":"2.6-fixture","ModuleName":"ADS"}`))
		case strings.HasSuffix(r.URL.Path, "/API/Core/Logout"):
			w.Write([]byte(`{"Status":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(ts.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600)
	return ts, ca, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return append([]map[string]any(nil), logins...) }
}

type panelClient struct {
	t    *testing.T
	base string
	c    *http.Client
}

func openTestPanel(t *testing.T, cfg panel.Config) *panelClient {
	t.Helper()
	u, e := panel.Open(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	jar, _ := cookiejar.New(nil)
	p, _ := url.Parse(u)
	pc := &panelClient{t: t, base: "http://" + p.Host, c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	if code, _ := pc.do(pc.c.Get(u)); code != 200 {
		t.Fatalf("launch status %d", code)
	}
	t.Cleanup(func() { pc.post("/close", url.Values{}) })
	return pc
}

func (p *panelClient) do(r *http.Response, e error) (int, string) {
	p.t.Helper()
	if e != nil {
		p.t.Fatal(e)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}
func (p *panelClient) get(path string) (int, string) { return p.do(p.c.Get(p.base + path)) }
func (p *panelClient) post(path string, v url.Values) (int, string) {
	return p.do(p.c.PostForm(p.base+path, v))
}

func TestConnectionPanelTestSaveEditDelete(t *testing.T) {
	tempStore(t)
	ts, ca, logins := fakeAMP(t)
	const secret = "synthetic-token-7f3e"
	p := openTestPanel(t, managerConfig())
	form := url.Values{"_key": {""}, "name": {"lab"}, "url": {ts.URL + "/"}, "username": {"admin"}, "authentication": {"service_login"},
		"credential": {"admin:" + secret}, "caCert": {ca}, "timeoutSeconds": {""}}

	// Test uses the unsaved values and writes nothing.
	form.Set("_op", "action:test")
	code, body := p.post("/submit", form)
	if code != 200 || !strings.Contains(body, "Connection successful") || strings.Contains(body, secret) {
		t.Fatalf("test action: %d %s", code, body)
	}
	if l := logins(); len(l) != 1 || l[0]["token"] != secret || l[0]["username"] != "admin" {
		t.Fatalf("draft login used %v", l)
	}
	if _, e := os.Stat(configPath()); !os.IsNotExist(e) {
		t.Fatal("Test saved the draft")
	}

	// Validation mirrors the former manager.
	for field, bad := range map[string]string{"url": "http://example.com", "timeoutSeconds": "301", "instanceId": "nope", "caCert": filepath.Join(ca, "missing")} {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set("_op", "save")
		f.Set(field, bad)
		if code, _ := p.post("/submit", f); code != 422 {
			t.Fatalf("%s=%s accepted (%d)", field, bad, code)
		}
	}
	f := url.Values{}
	for k, v := range form {
		f[k] = v
	}
	f.Set("_op", "save")
	f.Set("authentication", "password")
	if code, body := p.post("/submit", f); code != 422 || !strings.Contains(body, "requires an instance ID") {
		t.Fatalf("password without instance: %d", code)
	}

	form.Set("_op", "save")
	if code, body := p.post("/submit", form); code != 200 || !strings.Contains(body, "Saved lab") {
		t.Fatalf("save: %d %s", code, body)
	}
	raw, _ := os.ReadFile(configPath())
	if strings.Contains(string(raw), secret) {
		t.Fatal("plaintext credential in connections.json")
	}
	c, e := loadConfig()
	if e != nil {
		t.Fatal(e)
	}
	conn := c.Connections["lab"]
	if conn.URL != ts.URL || conn.TimeoutSeconds != 30 || conn.CACert != ca || conn.Password != "" {
		t.Fatalf("saved %+v", conn)
	}
	if plain, e := decryptToken(conn.Token); e != nil || plain != secret {
		t.Fatal("sealed token does not round-trip")
	}

	// The edit form never renders the stored secret; Test reuses it.
	if code, body := p.get("/edit?key=lab"); code != 200 || strings.Contains(body, secret) || strings.Contains(body, conn.Token) || !strings.Contains(body, "stored - leave blank") {
		t.Fatalf("edit page leaked or missing stored hint (%d)", code)
	}
	form.Del("credential")
	form.Set("_key", "lab")
	form.Set("_op", "action:test")
	if code, body := p.post("/submit", form); code != 200 || !strings.Contains(body, "Connection successful") {
		t.Fatalf("stored-credential test: %d %s", code, body)
	}
	if l := logins(); l[len(l)-1]["token"] != secret {
		t.Fatal("stored credential not used")
	}
	// Changing the mode needs the credential again.
	form.Set("_op", "save")
	form.Set("authentication", "password")
	form.Set("instanceId", "00000000-0000-0000-0000-000000000001")
	if code, body := p.post("/submit", form); code != 422 || !strings.Contains(body, "changing the authentication mode") {
		t.Fatalf("mode change kept old secret: %d", code)
	}
	// Rename keeps the stored token unchanged.
	form.Set("authentication", "service_login")
	form.Set("instanceId", "")
	form.Set("name", "lab2")
	if code, _ := p.post("/submit", form); code != 200 {
		t.Fatal("rename failed")
	}
	c, _ = loadConfig()
	if _, old := c.Connections["lab"]; old || c.Connections["lab2"].Token != conn.Token {
		t.Fatalf("rename result %+v", c.Connections)
	}

	if code, _ := p.post("/submit", url.Values{"_key": {"lab2"}, "_op": {"delete"}}); code != 200 {
		t.Fatal("delete failed")
	}
	if c, _ = loadConfig(); len(c.Connections) != 0 {
		t.Fatal("connection not deleted")
	}
}

func TestOpenConnectionManagerToolReturnsURL(t *testing.T) {
	tempStore(t)
	tool := internalTools(&App{clients: map[string]*Client{}})["amp_open_connection_manager"]
	r, e := tool.handler(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}})
	if e != nil || r.IsError {
		t.Fatalf("tool failed: %v", e)
	}
	var out struct{ URL string }
	json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &out)
	if !strings.HasPrefix(out.URL, "http://127.0.0.1:") || !strings.Contains(out.URL, "?t=") {
		t.Fatalf("url %q", out.URL)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	p := &panelClient{t: t, c: c, base: strings.Split(out.URL, "/?")[0]}
	if code, _ := p.do(c.Get(out.URL)); code != 200 {
		t.Fatal("launch failed")
	}
	done := make(chan struct{})
	go func() { panel.Wait(managerApp); close(done) }()
	p.post("/close", url.Values{})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
}

func TestGeneratedCredentialRevealPanel(t *testing.T) {
	tempStore(t)
	const secret = "Aa1!generated-secret"
	cipher, e := encryptCredential(secret)
	if e != nil {
		t.Fatal(e)
	}
	os.MkdirAll(credentialsDir(), 0700)
	file := filepath.Join(credentialsDir(), "user-1.json")
	if e = writeJSONAtomic(file, map[string]any{"version": 1, "connection": "lab", "instance_id": "", "username": "alice", "target": "x", "passwordDpapi": cipher}); e != nil {
		t.Fatal(e)
	}
	if _, e = readGeneratedFile("../connections.json"); e == nil {
		t.Fatal("path traversal accepted")
	}
	p := openTestPanel(t, credentialsConfig())
	if code, body := p.get("/"); code != 200 || !strings.Contains(body, "user-1.json") || strings.Contains(body, secret) || strings.Contains(body, "/edit\"") {
		t.Fatalf("list: %d", code)
	}
	if code, body := p.get("/edit?key=user-1.json"); code != 200 || strings.Contains(body, secret) || strings.Contains(body, `value="save"`) {
		t.Fatalf("edit page leaked or offers Save (%d)", code)
	}
	code, body := p.post("/submit", url.Values{"_key": {"user-1.json"}, "_op": {"action:reveal"}})
	if code != 200 || !strings.Contains(body, "Password for alice: "+secret) {
		t.Fatalf("reveal: %d", code)
	}
	if code, _ := p.post("/submit", url.Values{"_key": {"user-1.json"}, "_op": {"save"}}); code != 422 {
		t.Fatal("read-only panel accepted save")
	}
}

// AMP_MCP_REAL_CONFIG=<copy of a real connections.json> checks that an existing
// store opens unchanged. It never prints secrets or contacts AMP.
func TestRealConnectionsFileOpens(t *testing.T) {
	src := os.Getenv("AMP_MCP_REAL_CONFIG")
	if src == "" {
		t.Skip("AMP_MCP_REAL_CONFIG not set")
	}
	t.Setenv("AMP_MCP_CONFIG", src)
	c, e := loadConfig()
	if e != nil {
		t.Fatal(e)
	}
	recs, e := managerConfig().Load(context.Background())
	if e != nil || len(recs) != len(c.Connections) || len(recs) == 0 {
		t.Fatalf("panel load: %d records, %v", len(recs), e)
	}
	for name, conn := range c.Connections {
		sealed := conn.Token
		if conn.Password != "" {
			sealed = conn.Password
		}
		plain, e := decryptToken(sealed)
		if e != nil || plain == "" {
			t.Fatalf("connection %q: credential does not open", name)
		}
		if _, e = newClient(conn); e != nil {
			t.Fatalf("connection %q: %v", name, e)
		}
	}
	t.Logf("%d connections open", len(c.Connections))
}
