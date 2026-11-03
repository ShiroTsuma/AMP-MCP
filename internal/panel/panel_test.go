package panel

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() { OpenBrowser = func(string) error { return nil } }

// fakeStore is a minimal tool backend.
type fakeStore struct {
	mu    sync.Mutex
	recs  map[string]Record
	saves []Record
}

func (f *fakeStore) config(app string) Config {
	return Config{
		App: app, Title: "Fake", Key: "name",
		Fields: []Field{
			{Name: "name", Label: "Name", Type: Text, Required: true},
			{Name: "port", Label: "Port", Type: Number},
			{Name: "tls", Label: "TLS", Type: Checkbox},
			{Name: "mode", Label: "Mode", Type: Select, Options: []string{"ro", "rw"}},
			{Name: "password", Label: "Password", Type: Password, Required: true},
		},
		Load: func(context.Context) ([]Record, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			var out []Record
			for _, r := range f.recs {
				out = append(out, r)
			}
			return out, nil
		},
		Save: func(_ context.Context, oldKey string, rec Record) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.saves = append(f.saves, maps.Clone(rec))
			if _, ok := rec["password"]; !ok {
				rec["password"] = f.recs[oldKey]["password"] // keep stored secret
			}
			delete(f.recs, oldKey)
			f.recs[rec["name"]] = rec
			return nil
		},
		Delete: func(_ context.Context, key string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			delete(f.recs, key)
			return nil
		},
		Actions: []Action{{Name: "test", Label: "Test", Run: func(_ context.Context, key string, rec Record) (string, error) {
			if rec["port"] == "1" {
				return "", errors.New("connection refused")
			}
			return "connection OK for " + key, nil
		}}},
		IdleTimeout: time.Minute,
	}
}

func newClient(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

// body(t)(c.Get(u)) returns the status code and body text.
func body(t *testing.T) func(*http.Response, error) (int, string) {
	return func(resp *http.Response, err error) (int, string) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
}

func base(u string) string { p, _ := url.Parse(u); return "http://" + p.Host }

func open(t *testing.T, cfg Config) (string, *http.Client) {
	t.Helper()
	u, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	if code, _ := body(t)(c.Get(u)); code != 200 {
		t.Fatalf("launch URL status %d", code)
	}
	return u, c
}

func TestTokenRequiredAndOneTime(t *testing.T) {
	f := &fakeStore{recs: map[string]Record{}}
	u, c := open(t, f.config("t-token"))
	b := base(u)
	if code, _ := body(t)(newClient(t).Get(b + "/")); code != 403 {
		t.Fatalf("no cookie: %d", code)
	}
	if code, _ := body(t)(newClient(t).Get(b + "/?t=deadbeef")); code != 403 {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := body(t)(newClient(t).Get(u)); code != 403 {
		t.Fatalf("launch token reused: %d", code)
	}
	if code, _ := body(t)(c.Get(b + "/")); code != 200 {
		t.Fatalf("with cookie: %d", code)
	}
	// Single instance: second Open reuses the server with a new launch token.
	u2, err := Open(context.Background(), f.config("t-token"))
	if err != nil || base(u2) != b || u2 == u {
		t.Fatalf("second Open = %q, %v", u2, err)
	}
}

func TestHostAndOriginChecks(t *testing.T) {
	f := &fakeStore{recs: map[string]Record{}}
	u, c := open(t, f.config("t-host"))
	b := base(u)
	req, _ := http.NewRequest("GET", b+"/", nil)
	req.Host = "evil.example:" + strings.Split(b, ":")[2]
	if code, _ := body(t)(c.Do(req)); code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding Host: %d", code)
	}
	for hdr, val := range map[string]string{"Origin": "http://evil.example", "Sec-Fetch-Site": "cross-site"} {
		req, _ = http.NewRequest("POST", b+"/close", nil)
		req.Header.Set(hdr, val)
		if code, _ := body(t)(c.Do(req)); code != 403 {
			t.Fatalf("%s=%s: %d", hdr, val, code)
		}
	}
	code, page := body(t)(c.Get(b + "/"))
	if code != 200 {
		t.Fatalf("panel closed by rejected POST: %d", code)
	}
	_ = page
	resp, _ := c.Get(b + "/")
	for _, h := range []string{"Content-Security-Policy", "Cache-Control", "X-Frame-Options"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	resp.Body.Close()
}

func TestIdleShutdownAndClose(t *testing.T) {
	f := &fakeStore{recs: map[string]Record{}}
	cfg := f.config("t-idle")
	cfg.IdleTimeout = 200 * time.Millisecond
	u, c := open(t, cfg)
	time.Sleep(600 * time.Millisecond)
	if _, err := c.Get(base(u) + "/"); err == nil {
		t.Fatal("server still up after idle timeout")
	}
	cfg.IdleTimeout = time.Minute
	u, c = open(t, cfg)
	if base(u) == "" {
		t.Fatal("no URL after restart")
	}
	if code, page := body(t)(c.PostForm(base(u)+"/close", nil)); code != 200 || !strings.Contains(page, "stopped") {
		t.Fatalf("close: %d", code)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := c.Get(base(u) + "/"); err == nil {
		t.Fatal("server still up after close")
	}
}

func TestFormRoundTripAndPasswordNeverRendered(t *testing.T) {
	const secret = "Sup3r-S3cret-Value"
	f := &fakeStore{recs: map[string]Record{"prod": {"name": "prod", "port": "5432", "tls": "true", "mode": "ro", "password": secret}}}
	u, c := open(t, f.config("t-form"))
	b := base(u)

	_, list := body(t)(c.Get(b + "/"))
	_, form := body(t)(c.Get(b + "/edit?key=prod"))
	for _, p := range []string{list, form} {
		if strings.Contains(p, secret) {
			t.Fatal("stored password rendered")
		}
	}
	if !strings.Contains(form, `value="5432"`) || !strings.Contains(form, "checked") || !strings.Contains(form, "leave blank to keep") {
		t.Fatalf("form missing values:\n%s", form)
	}

	// Validation failure re-renders without echoing a typed password.
	code, page := body(t)(c.PostForm(b+"/submit", url.Values{"_key": {"prod"}, "_op": {"save"}, "name": {"prod"}, "port": {"abc"}, "password": {"typed-pw"}}))
	if code != 422 || !strings.Contains(page, "must be a number") || strings.Contains(page, "typed-pw") {
		t.Fatalf("validation: %d", code)
	}
	// Save with blank password keeps the stored one and renames the record.
	code, page = body(t)(c.PostForm(b+"/submit", url.Values{"_key": {"prod"}, "_op": {"save"}, "name": {"prod2"}, "port": {"6543"}, "mode": {"rw"}}))
	if code != 200 || !strings.Contains(page, "Saved prod2") {
		t.Fatalf("save: %d %s", code, page)
	}
	last := f.saves[len(f.saves)-1]
	if _, has := last["password"]; has {
		t.Fatal("blank password should be absent from saved record")
	}
	if got := f.recs["prod2"]; got["password"] != secret || got["tls"] != "" || got["mode"] != "rw" {
		t.Fatalf("stored record %v", got)
	}
	// Custom action.
	_, page = body(t)(c.PostForm(b+"/submit", url.Values{"_key": {"prod2"}, "_op": {"action:test"}, "name": {"prod2"}, "port": {"1"}}))
	if !strings.Contains(page, "connection refused") {
		t.Fatal("action error not shown")
	}
	_, page = body(t)(c.PostForm(b+"/submit", url.Values{"_key": {"prod2"}, "_op": {"action:test"}, "name": {"prod2"}, "port": {"2"}}))
	if !strings.Contains(page, "connection OK for prod2") {
		t.Fatal("action result not shown")
	}
	// New record requires a password; invalid select rejected.
	code, _ = body(t)(c.PostForm(b+"/submit", url.Values{"_op": {"save"}, "name": {"new"}}))
	if code != 422 {
		t.Fatalf("missing password accepted: %d", code)
	}
	code, _ = body(t)(c.PostForm(b+"/submit", url.Values{"_op": {"save"}, "name": {"new"}, "password": {"x"}, "mode": {"admin"}}))
	if code != 422 {
		t.Fatalf("invalid select accepted: %d", code)
	}
	// Delete.
	code, page = body(t)(c.PostForm(b+"/submit", url.Values{"_key": {"prod2"}, "_op": {"delete"}}))
	if code != 200 || len(f.recs) != 0 || !strings.Contains(page, "No entries") {
		t.Fatalf("delete: %d %v", code, f.recs)
	}
}

func TestUnlockPrompt(t *testing.T) {
	f := &fakeStore{recs: map[string]Record{}}
	cfg := f.config("t-unlock")
	locked := true
	cfg.Locked = func() bool { return locked }
	cfg.Unlock = func(p string) error {
		if p != "open sesame" {
			return errors.New("wrong vault passphrase")
		}
		locked = false
		return nil
	}
	u, c := open(t, cfg)
	b := base(u)
	if _, page := body(t)(c.Get(b + "/edit")); !strings.Contains(page, "Unlock secret vault") {
		t.Fatal("unlock prompt not shown")
	}
	if code, page := body(t)(c.PostForm(b+"/unlock", url.Values{"passphrase": {"nope"}})); code != 422 || !strings.Contains(page, "wrong vault passphrase") {
		t.Fatalf("wrong passphrase: %d", code)
	}
	if _, page := body(t)(c.PostForm(b+"/unlock", url.Values{"passphrase": {"open sesame"}})); !strings.Contains(page, "Vault unlocked") {
		t.Fatal("unlock failed")
	}
}

func TestRenderedPagesAndAssets(t *testing.T) {
	var launched []string
	OpenBrowser = func(u string) error { launched = append(launched, u); return nil }
	defer func() { OpenBrowser = func(string) error { return nil } }()
	f := &fakeStore{recs: map[string]Record{"prod": {"name": "prod", "port": "5432", "mode": "ro", "password": "x"}}}
	u, c := open(t, f.config("t-render"))
	if len(launched) != 1 || launched[0] != u {
		t.Fatalf("browser opener not injected: %v", launched)
	}
	b := base(u)
	pages := map[string][]string{
		"/":              {"<title>Fake</title>", "<th scope=\"col\">Name</th>", "href=\"/edit?key=prod\"", "href=\"/style.css\""},
		"/edit?key=prod": {`name="name"`, `type="number"`, `type="checkbox"`, "<select", "<option selected>ro</option>", `type="password"`, `value="action:test"`, `value="delete"`, ">Port<"},
		"/edit":          {"New entry", `name="password"`, "required"},
	}
	for path, want := range pages {
		resp, err := c.Get(b + path)
		ct := ""
		if err == nil {
			ct = resp.Header.Get("Content-Type")
		}
		code, page := body(t)(resp, err)
		if code != 200 || !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("%s: %d %q", path, code, ct)
		}
		if strings.Contains(page, "{{") || strings.Contains(page, "}}") || strings.Contains(page, "ZgotmplZ") {
			t.Errorf("%s: leftover template syntax:\n%s", path, page)
		}
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("%s: missing %q", path, w)
			}
		}
	}
	resp, err := c.Get(b + "/style.css")
	ct := ""
	if err == nil {
		ct = resp.Header.Get("Content-Type")
	}
	if code, css := body(t)(resp, err); code != 200 || !strings.HasPrefix(ct, "text/css") || !strings.Contains(css, "--accent") {
		t.Fatalf("style.css: %d %q", code, ct)
	}
}
