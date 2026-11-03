package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"amp-mcp/internal/panel"
	"amp-mcp/internal/paths"
	"amp-mcp/internal/secretstore"
)

// The connection manager is a local browser panel (internal/panel): 127.0.0.1
// only, one-time launch URL, no scripts. Stored credentials never reach the page.
const managerApp = "AMP-MCP"

var managerFields = []panel.Field{
	{Name: "name", Label: "Name", Type: panel.Text, Required: true},
	{Name: "url", Label: "HTTPS URL", Type: panel.Text, Required: true, Help: "Panel address, for example https://amp.example.com. No credentials, query or fragment."},
	{Name: "username", Label: "Username", Type: panel.Text, Required: true},
	{Name: "authentication", Label: "Authentication", Type: panel.Select, Required: true, Options: []string{"service_login", "password"},
		Help: "service_login: an AMP Service Login token (username:token is accepted). password: a native account password; requires the instance ID."},
	{Name: "credential", Label: "Token or password", Type: panel.Password, Required: true, Help: "Encrypted on save. Re-enter it when you change the authentication mode."},
	{Name: "instanceId", Label: "Instance ID (optional)", Type: panel.Text, Help: "Instance GUID. Empty targets ADS; required for password authentication."},
	{Name: "caCert", Label: "CA file (optional)", Type: panel.Text,
		Help: "PEM file of a private or homelab CA. Export it from your CA (Caddy, step-ca, router), or save the last certificate printed by: openssl s_client -showcerts -connect host:443. TLS verification cannot be disabled."},
	{Name: "timeoutSeconds", Label: "Timeout (seconds)", Type: panel.Number, Help: "1-300, default 30. Test uses at most 10."},
}

func managerConfig() panel.Config {
	return panel.Config{
		App: managerApp, Title: "AMP connections", Key: "name", Fields: managerFields,
		Load: func(context.Context) ([]panel.Record, error) {
			c, e := readConfigForEdit()
			if e != nil {
				return nil, e
			}
			names := make([]string, 0, len(c.Connections))
			for n := range c.Connections {
				names = append(names, n)
			}
			sort.Strings(names)
			recs := []panel.Record{}
			for _, n := range names {
				x := c.Connections[n]
				r := panel.Record{"name": n, "url": x.URL, "username": x.Username, "authentication": "service_login", "credential": x.Token,
					"instanceId": x.InstanceID, "caCert": x.CACert, "timeoutSeconds": strconv.Itoa(x.TimeoutSeconds)}
				if x.TimeoutSeconds == 0 {
					r["timeoutSeconds"] = "30"
				}
				if x.Password != "" {
					r["authentication"], r["credential"] = "password", x.Password
				}
				recs = append(recs, r)
			}
			return recs, nil
		},
		Save:    saveConnection,
		Delete:  deleteConnection,
		Actions: []panel.Action{{Name: "test", Label: "Test (unsaved values)", Run: testDraft}},
		Locked:  func() bool { st, e := secrets(); return e == nil && secretstore.VaultLocked(st) },
		Unlock: func(p string) error {
			st, e := secrets()
			if e != nil {
				return e
			}
			return secretstore.UnlockVault(st, p)
		},
	}
}

func openManager(ctx context.Context) (string, error) { return panel.Open(ctx, managerConfig()) }

// readConfigForEdit is loadConfig, but a missing file is an empty store.
func readConfigForEdit() (Config, error) {
	if _, e := os.Stat(configPath()); errors.Is(e, os.ErrNotExist) {
		return Config{Version: 1, Connections: map[string]Connection{}}, nil
	}
	c, e := loadConfig()
	if e == nil && c.Connections == nil {
		c.Connections = map[string]Connection{}
	}
	return c, e
}

// draftConnection validates a panel record like the former PowerShell manager.
// It returns the connection (with the stored secret when none was typed) and
// the newly typed plaintext credential, if any.
func draftConnection(c Config, oldKey string, rec panel.Record) (string, Connection, string, error) {
	var conn Connection
	name, user := strings.TrimSpace(rec["name"]), strings.TrimSpace(rec["username"])
	if name == "" || user == "" {
		return "", conn, "", errors.New("name and username are required")
	}
	if _, dup := c.Connections[name]; dup && name != oldKey {
		return "", conn, "", errors.New("connection name already exists")
	}
	u, e := url.Parse(strings.TrimSpace(rec["url"]))
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", conn, "", errors.New("enter an HTTPS URL without credentials, query or fragment")
	}
	timeout := 30
	if t := strings.TrimSpace(rec["timeoutSeconds"]); t != "" {
		if timeout, e = strconv.Atoi(t); e != nil || timeout < 1 || timeout > 300 {
			return "", conn, "", errors.New("timeout must be 1-300 seconds")
		}
	}
	ca, _ := paths.ExpandHome(strings.TrimSpace(rec["caCert"]))
	if ca != "" {
		if st, e := os.Stat(ca); e != nil || !st.Mode().IsRegular() {
			return "", conn, "", errors.New("CA file does not exist")
		}
	}
	instance := strings.TrimSpace(rec["instanceId"])
	if instance != "" && !validSetupGUID(instance) {
		return "", conn, "", errors.New("instance ID must be a GUID, or empty for ADS")
	}
	mode := rec["authentication"]
	if mode != "service_login" && mode != "password" {
		return "", conn, "", errors.New("authentication must be service_login or password")
	}
	if mode == "password" && instance == "" {
		return "", conn, "", errors.New("password authentication requires an instance ID")
	}
	conn = Connection{URL: strings.TrimRight(u.String(), "/"), Username: user, CACert: ca, TimeoutSeconds: timeout, InstanceID: instance}
	plain, typed := rec["credential"]
	if typed {
		if mode == "service_login" {
			plain = strings.TrimSpace(plain)
			if prefix, token, ok := strings.Cut(plain, ":"); ok {
				if prefix != user {
					return "", conn, "", errors.New("token username does not match")
				}
				plain = token
			}
		}
		if plain == "" {
			return "", conn, "", errors.New("token or password is required")
		}
		return name, conn, plain, nil
	}
	old, ok := c.Connections[oldKey]
	if oldKey == "" || !ok {
		return "", conn, "", errors.New("token or password is required")
	}
	if (old.Password != "") != (mode == "password") {
		return "", conn, "", errors.New("enter the token or password again when changing the authentication mode")
	}
	conn.Token, conn.Password = old.Token, old.Password
	return name, conn, "", nil
}

func saveConnection(_ context.Context, oldKey string, rec panel.Record) error {
	unlock, e := lockFile(configPath() + ".lock")
	if e != nil {
		return errors.New("connection store is busy; try again")
	}
	defer unlock()
	c, e := readConfigForEdit()
	if e != nil {
		return e
	}
	name, conn, plain, e := draftConnection(c, oldKey, rec)
	if e != nil {
		return e
	}
	old := c.Connections[oldKey]
	if plain != "" {
		sealed, e := encryptCredential(plain)
		if e != nil {
			return e
		}
		if rec["authentication"] == "password" {
			conn.Password = sealed
		} else {
			conn.Token = sealed
		}
	}
	delete(c.Connections, oldKey)
	c.Connections[name] = conn
	if e = writeJSONAtomic(configPath(), c); e != nil {
		if plain != "" {
			forgetCredential(conn.Token + conn.Password)
		}
		return e
	}
	for _, s := range []string{old.Token, old.Password} {
		if s != "" && s != conn.Token && s != conn.Password {
			forgetCredential(s)
		}
	}
	return nil
}

func deleteConnection(_ context.Context, key string) error {
	unlock, e := lockFile(configPath() + ".lock")
	if e != nil {
		return errors.New("connection store is busy; try again")
	}
	defer unlock()
	c, e := readConfigForEdit()
	if e != nil {
		return e
	}
	old, ok := c.Connections[key]
	if !ok {
		return errors.New("connection not found; reload the list")
	}
	delete(c.Connections, key)
	if e = writeJSONAtomic(configPath(), c); e != nil {
		return e
	}
	forgetCredential(old.Token)
	forgetCredential(old.Password)
	return nil
}

// testDraft checks the unsaved form values; a typed credential is used in
// memory only and never sealed or written.
func testDraft(ctx context.Context, key string, rec panel.Record) (string, error) {
	c, e := readConfigForEdit()
	if e != nil {
		return "", e
	}
	_, conn, plain, e := draftConnection(c, key, rec)
	if e != nil {
		return "", e
	}
	if plain != "" {
		conn.Token, conn.Password, conn.draft = "", "", plain
		if rec["authentication"] == "password" {
			conn.Password = "unsaved" // marks password mode; the draft value is used
		}
	}
	conn.TimeoutSeconds = min(conn.TimeoutSeconds, 10)
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	cl, e := newClient(conn)
	if e != nil {
		return "", e
	}
	defer cl.close()
	v, e := cl.call(ctx, "", "Core/GetModuleInfo", map[string]any{})
	if e != nil {
		return "", fmt.Errorf("connection failed: %v; check address, credential, permissions and certificate trust", e)
	}
	m, _ := v.(map[string]any)
	return fmt.Sprintf("Connection successful (not saved): AMP %v, module %v.", m["AMPVersion"], m["ModuleName"]), nil
}

// Generated credentials (amp_user reset_password, amp_setting generate_secret)
// are shown to the human in a read-only panel: a value is rendered only after
// an explicit Reveal click, in a no-store page, and never through MCP or stdout.
const credentialsApp = "AMP-MCP-credentials"

func credentialsDir() string { return filepath.Join(filepath.Dir(configPath()), "credentials") }

type generatedFile struct {
	Version    int
	Kind       string
	Connection string
	Instance   string `json:"instance_id"`
	Username   string
	Cipher     string `json:"passwordDpapi"`
}

func readGeneratedFile(base string) (generatedFile, error) {
	var g generatedFile
	if base != filepath.Base(base) || !strings.HasSuffix(base, ".json") || strings.ContainsAny(base, `/\`) {
		return g, errors.New("invalid credential file name")
	}
	p := filepath.Join(credentialsDir(), base)
	info, e := os.Lstat(p)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return g, errors.New("invalid generated credential file")
	}
	b, e := os.ReadFile(p)
	if e != nil || json.Unmarshal(b, &g) != nil || g.Version != 1 || g.Cipher == "" {
		return g, errors.New("invalid generated credential file")
	}
	return g, nil
}

func credentialsConfig() panel.Config {
	cfg := managerConfig()
	return panel.Config{
		App: credentialsApp, Title: "AMP generated credentials", Key: "file", ReadOnly: true,
		Fields: []panel.Field{
			{Name: "file", Label: "File", Type: panel.Text},
			{Name: "username", Label: "User or setting", Type: panel.Text},
			{Name: "connection", Label: "Connection", Type: panel.Text},
			{Name: "instance_id", Label: "Instance", Type: panel.Text},
		},
		Load: func(context.Context) ([]panel.Record, error) {
			entries, e := os.ReadDir(credentialsDir())
			if errors.Is(e, os.ErrNotExist) {
				return nil, nil
			}
			if e != nil {
				return nil, e
			}
			recs := []panel.Record{}
			for _, en := range entries {
				if g, e := readGeneratedFile(en.Name()); e == nil {
					recs = append(recs, panel.Record{"file": en.Name(), "username": g.Username, "connection": g.Connection, "instance_id": g.Instance})
				}
			}
			return recs, nil
		},
		Save: func(context.Context, string, panel.Record) error {
			return errors.New("generated credentials are read-only")
		},
		Actions: []panel.Action{{Name: "reveal", Label: "Reveal password", Run: func(_ context.Context, key string, _ panel.Record) (string, error) {
			g, e := readGeneratedFile(key)
			if e != nil {
				return "", e
			}
			plain, e := decryptToken(g.Cipher)
			if e != nil {
				return "", e
			}
			return "Password for " + g.Username + ": " + plain, nil
		}}},
		Locked: cfg.Locked, Unlock: cfg.Unlock,
	}
}

// openAndWait runs a panel until the user closes it or it idles out.
func openAndWait(cfg panel.Config) error {
	u, e := panel.Open(context.Background(), cfg)
	if e != nil {
		return e
	}
	fmt.Println("Open this one-time link in a browser on this machine:")
	fmt.Println(u)
	fmt.Println("Waiting until you press Close panel (or 10 minutes without activity).")
	panel.Wait(cfg.App)
	return nil
}
