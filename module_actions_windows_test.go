//go:build windows

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// One integration contract: actual tools/list and tools/call, schema validation,
// fixed catalog, no credential passthrough, no write replay, stale schema.
// The API is synthetic; this never accepts the real Minecraft EULA.
func TestCriticalModuleActionContract(t *testing.T) {
	t.Setenv("AMP_MCP_CONFIG", filepath.Join(t.TempDir(), "connections.json"))
	scope := "00000000-0000-0000-0000-000000000001"
	var writes atomic.Int32
	var changed atomic.Bool
	var badArguments atomic.Bool
	makeMethod := func(params []any) map[string]any {
		return map[string]any{"Parameters": params, "ReturnTypeName": "ActionResult", "RequiredPermissions": []any{"Fixture.Control"}}
	}
	param := func(name, typ string, optional bool) map[string]any {
		return map[string]any{"Name": name, "TypeName": typ, "Optional": optional}
	}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		prefix := "/API/ADSModule/Servers/" + scope + "/API/"
		switch r.URL.Path {
		case prefix + "Core/GetAPISpec":
			version := param("Count", "Int32", false)
			if changed.Load() {
				version = param("Count", "String", false)
			}
			mode := param("Mode", "FixtureMode", false)
			mode["ParamEnumValues"] = map[string]any{"Small": float64(1), "Large": float64(2)}
			methods := map[string]any{"AcceptEULA": makeMethod([]any{}), "FixtureAction": makeMethod([]any{version, mode}), "SetPassword": makeMethod([]any{param("Password", "String", false)}), "UnknownRecord": makeMethod([]any{param("Object", "CustomRecord", false)})}
			json.NewEncoder(w).Encode(map[string]any{"MinecraftModule": methods})
		case prefix + "MinecraftModule/FixtureAction":
			writes.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["Count"] != float64(3) || body["Mode"] != float64(1) {
				badArguments.Store(true)
			}
			w.Write([]byte(`{"Title":"Unauthorized Access","Message":"requires Session.Exists permission"}`))
		default:
			t.Errorf("unexpected remote call: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer ts.Close()
	conn := Connection{URL: ts.URL, Username: "fixture"}
	if e := writeJSONAtomic(configPath(), Config{Version: 1, Connections: map[string]Connection{"fixture": conn}}); e != nil {
		t.Fatal(e)
	}
	cl, e := newClient(conn)
	if e != nil {
		t.Fatal(e)
	}
	cl.http = ts.Client()
	cl.sessions[scope] = "fixture-session"
	app := &App{clients: map[string]*Client{"fixture": cl}}
	srv := server(app)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, e := srv.Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	mc := mcp.NewClient(&mcp.Implementation{Name: "critical-contract", Version: "1"}, nil)
	cs, e := mc.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	target := func(method string) map[string]any {
		return map[string]any{"connection": "fixture", "instance_id": scope, "module": "MinecraftModule", "method": method}
	}
	describe := func(method string) map[string]any {
		args := target(method)
		args["operation"] = "describe"
		return args
	}
	before, e := cs.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	setupArgs := map[string]any{"operation": "preview", "connection": "fixture", "target_connection": "fixture", "profile": "minecraft-manager", "scope": "instance", "role_name": "synthetic", "application_instance_ids": []string{scope}}
	if !call("amp_account_setup_read", setupArgs).IsError {
		t.Fatal("game-specific profile was accepted")
	}
	setupArgs["profile"] = "operator"
	setupArgs["application_instance_ids"] = []string{scope, "00000000-0000-0000-0000-000000000002"}
	if !call("amp_account_setup_read", setupArgs).IsError {
		t.Fatal("local setup accepted multiple instances")
	}
	if !call("amp_module_action_read", describe("SetPassword")).IsError {
		t.Fatal("credential operation exposed")
	}
	listing := call("amp_module_action_read", map[string]any{"operation": "list", "connection": "fixture", "instance_id": scope, "module": "MinecraftModule"})
	if listing.IsError {
		t.Fatal(listing)
	}
	var discovered struct {
		Items []struct {
			Method    string
			Supported bool
		}
	}
	if e = json.Unmarshal([]byte(listing.Content[0].(*mcp.TextContent).Text), &discovered); e != nil {
		t.Fatal(e)
	}
	blocked := 0
	for _, item := range discovered.Items {
		if item.Method == "SetPassword" || item.Method == "UnknownRecord" {
			if item.Supported {
				t.Fatal("unsafe or untyped action advertised as supported")
			}
			blocked++
		}
	}
	if blocked != 2 {
		t.Fatal("unsupported module methods were not reported")
	}
	described := call("amp_module_action_read", describe("FixtureAction"))
	if described.IsError {
		t.Fatal(described)
	}
	var contract struct {
		ID string `json:"contract_id"`
	}
	if e = json.Unmarshal([]byte(described.Content[0].(*mcp.TextContent).Text), &contract); e != nil || contract.ID == "" {
		t.Fatal("missing action contract")
	}
	run := func(args map[string]any) *mcp.CallToolResult {
		request := target("FixtureAction")
		request["contract_id"] = contract.ID
		request["arguments"] = args
		return call("amp_run_module_action", request)
	}
	exposed, e := cs.ListTools(ctx, nil)
	if e != nil || len(exposed.Tools) != len(before.Tools) {
		t.Fatal("module discovery changed tools/list")
	}
	for _, tool := range exposed.Tools {
		if tool.OutputSchema != nil {
			t.Fatal("unexpected output schema")
		}
	}
	for _, args := range []map[string]any{{"Count": "bad", "Mode": "Small"}, {"Count": 3, "Mode": "unknown"}, {"Count": 3, "Mode": "Small", "unexpected": true}, {"Mode": "Small"}} {
		if !run(args).IsError {
			t.Fatal("invalid arguments accepted")
		}
	}
	// Prove validation now applies to the existing static tools as well.
	if !call("amp_instance", map[string]any{"operation": "start", "connection": "fixture", "instance_id": scope, "unexpected": true}).IsError {
		t.Fatal("static tool accepted unknown field")
	}
	if writes.Load() != 0 {
		t.Fatal("invalid or description requests invoked a module mutation")
	}
	r := run(map[string]any{"Count": 3, "Mode": "Small"})
	if !r.IsError || writes.Load() != 1 || badArguments.Load() {
		t.Fatal("argument translation or no-replay contract failed")
	}
	cl.mu.Lock()
	cl.sessions[scope] = "fixture-session"
	cl.mu.Unlock()
	changed.Store(true)
	if !run(map[string]any{"Count": 3, "Mode": "Small"}).IsError || writes.Load() != 1 {
		t.Fatal("stale module contract was executed")
	}
}
