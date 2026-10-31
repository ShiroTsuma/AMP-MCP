//go:build windows

package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// One risk-focused contract: failed verification must not revoke bootstrap
// access; an uncertain/rejected grant must be journaled and never replayed.
func TestCriticalAccountSetupFailureAndRevocationOrder(t *testing.T) {
	t.Setenv("AMP_MCP_CONFIG", filepath.Join(t.TempDir(), "connections.json"))
	const scope = "00000000-0000-0000-0000-000000000001"
	const role = "00000000-0000-0000-0000-000000000002"
	const root = "00000000-0000-0000-0000-000000000003"
	var grants, revokes atomic.Int32
	var changedACL atomic.Bool
	var denyActor atomic.Bool
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		endpoint := r.URL.Path[strings.LastIndex(r.URL.Path, "/API/")+5:]
		switch endpoint {
		case "Core/Login":
			w.Write([]byte(`{"success":true,"sessionID":"synthetic-session"}`))
		case "Core/Logout":
			w.Write([]byte(`{"Status":true}`))
		case "Core/GetAPISpec":
			json.NewEncoder(w).Encode(map[string]any{"Core": map[string]any{"GetModuleInfo": map[string]any{}, "GetStatus": map[string]any{}, "CurrentSessionHasPermission": map[string]any{}, "SetAMPRolePermission": map[string]any{}, "SetAMPUserRoleMembership": map[string]any{}, "GetRoleData": map[string]any{}, "GetAMPUserInfo": map[string]any{}, "GetPermissionsSpec": map[string]any{}, "GetAMPRolePermissions": map[string]any{}}, "ADSModule": map[string]any{"ManageInstance": map[string]any{}}, "SyntheticGame": map[string]any{"ManageGame": map[string]any{"RequiredPermissions": []string{"SharedGame.CustomAction", "Core.RoleManagement.EditRolePermissions", "Settings.Core.Security.Password"}}}})
		case "Core/GetAMPUserInfo":
			json.NewEncoder(w).Encode(map[string]any{"Roles": []string{role}})
		case "Core/GetRoleData":
			json.NewEncoder(w).Encode([]any{map[string]any{"ID": role, "IsInstanceSpecific": true, "Members": []any{map[string]any{"ID": root}}}})
		case "Core/GetPermissionsSpec":
			p, _ := profileByID("operator")
			nodes := []any{}
			for _, n := range p.Application {
				nodes = append(nodes, map[string]any{"Node": n, "Children": []any{}})
			}
			for _, n := range []string{"FileManager.FileManager.UploadFiles", "FileManager.FileManager.ModifyAMPConfigFiles", "Settings.SyntheticGame.Server.Name", "Settings.Meta.SyntheticGame.Map", "SharedGame.CustomAction", "Settings.OtherGame.Server.Name", "Settings.Core.Security.Password", "Core.RoleManagement.EditRolePermissions", "Core.UserManagement.CreateNewUser"} {
				nodes = append(nodes, map[string]any{"Node": n, "Children": []any{}})
			}
			json.NewEncoder(w).Encode(nodes)
		case "Core/GetAMPRolePermissions":
			if changedACL.Load() {
				w.Write([]byte(`["Core.AppManagement.ReadConsole"]`))
			} else {
				w.Write([]byte(`[]`))
			}
		case "Core/GetModuleInfo":
			if strings.Contains(r.URL.Path, "/Servers/") {
				w.Write([]byte(`{"ModuleName":"SyntheticGame"}`))
			} else {
				json.NewEncoder(w).Encode(map[string]any{"InstanceId": root})
			}
		case "ADSModule/ManageInstance":
			w.Write([]byte(`{"Status":true,"Result":"synthetic-delegation"}`))
		case "Core/CurrentSessionHasPermission":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if node, _ := body["PermissionNode"].(string); denyActor.Load() && (strings.HasPrefix(node, "Core.AppManagement.") || node == "*") {
				w.Write([]byte(`false`))
			} else {
				w.Write([]byte(`true`))
			}
		case "Core/GetStatus":
			w.WriteHeader(http.StatusForbidden)
		case "Core/SetAMPUserRoleMembership":
			revokes.Add(1)
			w.Write([]byte(`{"Status":true}`))
		case "Core/SetAMPRolePermission":
			grants.Add(1)
			w.Write([]byte(`{"Title":"Unauthorized Access","Message":"requires Session.Exists permission"}`))
		default:
			t.Errorf("unexpected endpoint: %s", endpoint)
			w.WriteHeader(500)
		}
	}))
	defer ts.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	admin := Connection{URL: ts.URL, Username: "bootstrap", Token: fixtureToken(t), CACert: ca}
	target := admin
	target.Username = "service"
	if e := writeJSONAtomic(configPath(), Config{Version: 1, Connections: map[string]Connection{"bootstrap": admin, "service": target}}); e != nil {
		t.Fatal(e)
	}
	a := &App{clients: map[string]*Client{}}
	defer func() {
		for _, cl := range a.clients {
			cl.close()
		}
	}()
	r := setupReceipt{ID: strings.Repeat("a", 32), RoleID: role, State: "configured", Plan: setupPlan{Input: setupInput{Connection: "bootstrap", TargetConnection: "service", Scope: "instance", Profile: "operator", Instances: []string{scope}, BootstrapRole: role}, BootstrapTarget: targetFingerprint(admin), Target: targetFingerprint(target), Grants: []scopeGrant{{Nodes: []string{"Instances." + scope + ".Manage"}}, {Instance: scope, Module: "SyntheticGame", Nodes: []string{"Core.AppManagement.ReadConsole"}}}}}
	if e := saveSetup(r); e != nil {
		t.Fatal(e)
	}
	if _, e := a.finalizeAccountSetup(context.Background(), "bootstrap", r.ID, []string{scope}); e == nil {
		t.Fatal("failed status verification accepted")
	}
	saved, e := loadSetup(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if saved.State != "configured" || revokes.Load() != 0 {
		t.Fatal("bootstrap access changed before verification passed")
	}
	cl, e := a.client("bootstrap")
	if e != nil {
		t.Fatal(e)
	}
	if e = checkProfileNodes(context.Background(), cl, "", []string{"Core.*"}); e != nil {
		t.Fatalf("advertised ADS admin namespace rejected: %v", e)
	}
	if e = checkProfileNodes(context.Background(), cl, "", []string{"Unadvertised.*"}); e == nil {
		t.Fatal("unadvertised ADS namespace accepted")
	}
	if _, e = setupMutation(context.Background(), cl, &r, "", "Core/SetAMPRolePermission", map[string]any{"RoleId": role, "PermissionNode": "Core.AppManagement.ReadConsole", "Enabled": true}); e == nil {
		t.Fatal("failed grant accepted")
	}
	if grants.Load() != 1 {
		t.Fatal("grant was replayed")
	}
	jobs, e := readJobs()
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Jobs) != 1 || jobs[r.Jobs[0]].State != "failed" {
		t.Fatal("failed grant not journaled")
	}
	r.State = "complete"
	r.Plan.UserID = root
	if e = saveSetup(r); e != nil {
		t.Fatal(e)
	}
	x := expandInput{Connection: "bootstrap", SetupID: r.ID, Instance: scope, Module: "SyntheticGame", Mode: "preview"}
	p, _, e := a.planPrivilegeExpansion(context.Background(), x)
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range []string{"Core.AppManagement.SendConsoleInput", "FileManager.FileManager.UploadFiles", "Settings.SyntheticGame.Server.Name", "Settings.Meta.SyntheticGame.Map", "SharedGame.CustomAction"} {
		if !roleAllows(p.Desired, node) {
			t.Fatalf("operator cannot manage its module: %s", node)
		}
	}
	for _, node := range []string{"Core.RoleManagement.EditRolePermissions", "Core.UserManagement.CreateNewUser", "Settings.Core.Security.Password", "FileManager.FileManager.ModifyAMPConfigFiles", "Settings.OtherGame.Server.Name"} {
		if roleAllows(p.Desired, node) {
			t.Fatalf("operator received AMP administration or another module: %s", node)
		}
	}
	changedACL.Store(true)
	x.Mode = "apply"
	x.PlanID = p.PlanID
	if _, e = a.expandPrivileges(context.Background(), x); e == nil || !strings.Contains(e.Error(), "stale") {
		t.Fatalf("stale expansion accepted: %v", e)
	}
	if grants.Load() != 1 {
		t.Fatal("stale expansion changed permissions")
	}
	denyActor.Store(true)
	p, _, e = a.planPrivilegeExpansion(context.Background(), x)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.MissingActor) == 0 {
		t.Fatal("missing actor permissions were not reported")
	}
	x.PlanID = p.PlanID
	if _, e = a.expandPrivileges(context.Background(), x); e == nil || !strings.Contains(e.Error(), "bootstrap") {
		t.Fatalf("missing actor rights did not stop expansion: %v", e)
	}
	if grants.Load() != 1 {
		t.Fatal("expansion wrote without actor permissions")
	}
	denyActor.Store(false)
	p, _, e = a.planPrivilegeExpansion(context.Background(), x)
	if e != nil {
		t.Fatal(e)
	}
	x.PlanID = p.PlanID
	if _, e = a.expandPrivileges(context.Background(), x); e == nil {
		t.Fatal("failed expansion accepted")
	}
	if grants.Load() != 2 {
		t.Fatal("expansion mutation was replayed")
	}
	x.Instance = "00000000-0000-0000-0000-000000000009"
	if _, _, e = a.planPrivilegeExpansion(context.Background(), x); e == nil {
		t.Fatal("local profile expanded to another instance")
	}
	// Regression: admin means authority over future namespaces, not the
	// currently advertised set. Lacking that authority must stop before writes.
	r.Plan.Input.Profile = "admin"
	r.Plan.Profile, _ = profileByID("admin")
	if e = saveSetup(r); e != nil {
		t.Fatal(e)
	}
	denyActor.Store(true)
	x.Instance = scope
	p, _, e = a.planPrivilegeExpansion(context.Background(), x)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Missing) != 1 || p.Missing[0] != "*" || len(p.MissingActor) != 1 || p.MissingActor[0] != "*" {
		t.Fatal("admin was downgraded to a current permission catalog")
	}
	x.PlanID = p.PlanID
	if _, e = a.expandPrivileges(context.Background(), x); e == nil || grants.Load() != 2 {
		t.Fatal("admin authority expanded without a full-scope bootstrap")
	}
	for _, tt := range []struct {
		acl   []string
		node  string
		allow bool
	}{
		{[]string{"*"}, "FutureModule.NewAction", true},
		{[]string{"Settings.Meta.GenericModule.*"}, "Settings.Meta.GenericModule.FutureSetting", true},
		{[]string{"Core.*", "-Core.AppManagement.StopApplication"}, "Core.AppManagement.StopApplication", false},
		{[]string{"-Core.*", "Core.AppManagement.StartApplication"}, "Core.AppManagement.StartApplication", true},
		{[]string{"Core.AppManagement.StartApplication", "-Core.AppManagement.StartApplication"}, "Core.AppManagement.StartApplication", false},
	} {
		if roleAllows(tt.acl, tt.node) != tt.allow {
			t.Fatal("AMP wildcard/denial precedence mismatch")
		}
	}
}
