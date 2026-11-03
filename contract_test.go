package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The public contract stays small, annotated and consistently named.
func TestPublicToolContract(t *testing.T) {
	t.Setenv("AMP_MCP_CONFIG", filepath.Join(t.TempDir(), "connections.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, e := server(&App{clients: map[string]*Client{}}).Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	cs, e := mcp.NewClient(&mcp.Implementation{Name: "contract", Version: "1"}, nil).Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	if !strings.Contains(cs.InitializeResult().Instructions, "next_offset") {
		t.Fatal("server instructions missing")
	}
	list, e := cs.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if n := len(list.Tools); n > 40 || n < 20 {
		t.Fatalf("tool count %d outside 20-40", n)
	}
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	descriptions := map[string]string{}
	for _, tool := range list.Tools {
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil {
			t.Fatalf("%s lacks annotations", tool.Name)
		}
		if a.ReadOnlyHint && *a.DestructiveHint {
			t.Fatalf("%s is read-only and destructive", tool.Name)
		}
		if other, dup := descriptions[tool.Description]; dup || tool.Description == "" {
			t.Fatalf("%s shares its description with %s", tool.Name, other)
		}
		descriptions[tool.Description] = tool.Name
		schema := tool.InputSchema.(map[string]any)
		for name := range schema["properties"].(map[string]any) {
			if !snake.MatchString(name) {
				t.Fatalf("%s parameter %s is not snake_case", tool.Name, name)
			}
		}
		if strings.HasSuffix(tool.Name, "_read") != a.ReadOnlyHint && tool.Name != "amp_list_connections" && tool.Name != "amp_test_connection" {
			t.Fatalf("%s: read-only hint does not match its name", tool.Name)
		}
	}
	b, _ := json.Marshal(list.Tools)
	t.Logf("tools=%d contract=%d chars", len(list.Tools), len(b))

	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		r, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		return r.Content[0].(*mcp.TextContent).Text, r.IsError
	}
	guid := "00000000-0000-0000-0000-000000000001"
	for _, tt := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"amp_instance", map[string]any{"connection": "x", "operation": "start"}, "amp_instance operation=start requires: instance_id"},
		{"amp_file_read", map[string]any{"connection": "x", "operation": "list", "instance_id": guid, "path": "", "lines": 5}, "does not use lines"},
		{"amp_file_read", map[string]any{"connection": "x", "operation": "read", "instance_id": guid, "path": "a.txt", "chunk_size": 0}, "invalid arguments for amp_file_read"},
		{"amp_job_read", map[string]any{"connection": "x", "operation": "get", "job_id": "missing", "refresh": true}, "job not found"},
		{"amp_account_setup", map[string]any{"connection": "x", "operation": "expand_apply", "instance_id": guid, "module": "MinecraftModule"}, "requires: plan_id"},
	} {
		text, isError := call(tt.tool, tt.args)
		if !isError || !strings.Contains(text, tt.want) {
			t.Fatalf("%s %v: got %q", tt.tool, tt.args, text)
		}
	}
	text, isError := call("amp_job_read", map[string]any{"connection": "x", "operation": "list"})
	if isError || !strings.Contains(text, `"jobs":[]`) {
		t.Fatalf("job list dispatch failed: %s", text)
	}
	if publicToolName("amp_delete_instance") != "amp_instance operation=delete" {
		t.Fatal("job tool names are not public names")
	}
}

// UpdateUserInfo replaces all flags, so omitted ones must keep current values.
func TestUpdateUserKeepsOmittedFlags(t *testing.T) {
	reply := `{"Disabled":false,"PasswordExpires":true,"CannotChangePassword":false,"MustChangePassword":false}`
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	defer ts.Close()
	cl, e := newClient(Connection{URL: ts.URL, Username: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	cl.http = ts.Client()
	cl.sessions[""] = "fixture-session"
	cl.capabilities[""] = map[string]any{"Core": map[string]any{"GetAMPUserInfo": map[string]any{}}}
	body := map[string]any{"Username": "u", "Disabled": true}
	if e = fillUserFlags(context.Background(), cl, "", body); e != nil {
		t.Fatal(e)
	}
	if body["Disabled"] != true || body["PasswordExpires"] != true || body["MustChangePassword"] != false {
		t.Fatalf("flags not merged: %v", body)
	}
	reply = `{"Disabled":false}`
	if e = fillUserFlags(context.Background(), cl, "", map[string]any{"Username": "u"}); e == nil {
		t.Fatal("guessed a flag AMP did not report")
	}
}
