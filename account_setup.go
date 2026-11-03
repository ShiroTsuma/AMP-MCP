package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed permission_profiles.json
var permissionProfilesJSON []byte

type permissionProfile struct {
	ID               string   `json:"id"`
	Description      string   `json:"description"`
	Application      []string `json:"application"`
	FullScope        bool     `json:"full_scope"`
	ModuleManagement bool     `json:"module_management,omitempty"`
}
type setupInput struct {
	Connection       string   `json:"connection"`
	TargetConnection string   `json:"target_connection"`
	Profile          string   `json:"profile"`
	Scope            string   `json:"scope"`
	RoleName         string   `json:"role_name"`
	Instances        []string `json:"application_instance_ids"`
	BootstrapRole    string   `json:"bootstrap_role_id,omitempty"`
	PlanID           string   `json:"plan_id,omitempty"`
}
type scopeGrant struct {
	Instance string   `json:"instance_id,omitempty"`
	Module   string   `json:"module,omitempty"`
	Subject  string   `json:"subject"`
	Nodes    []string `json:"permissions"`
}
type setupPlan struct {
	Input             setupInput        `json:"input"`
	Profile           permissionProfile `json:"profile"`
	Grants            []scopeGrant      `json:"grants"`
	UserID            string            `json:"user_id"`
	BootstrapTarget   string            `json:"bootstrap_target"`
	Target            string            `json:"target"`
	Roles             []string          `json:"existing_roles"`
	ExistingInstances []string          `json:"existing_instances"`
	ADSInstanceID     string            `json:"ads_instance_id"`
	PlanID            string            `json:"plan_id"`
}
type setupReceipt struct {
	ID                    string    `json:"setup_id"`
	Operation             string    `json:"operation,omitempty"`
	Plan                  setupPlan `json:"plan"`
	RoleID                string    `json:"role_id,omitempty"`
	State                 string    `json:"state"`
	Step                  string    `json:"step"`
	Jobs                  []string  `json:"jobs"`
	VerificationInstances []string  `json:"verification_instance_ids,omitempty"`
}

func permissionProfiles() []permissionProfile {
	var p []permissionProfile
	if e := json.Unmarshal(permissionProfilesJSON, &p); e != nil {
		panic(e)
	}
	return p
}
func profileByID(id string) (permissionProfile, error) {
	for _, p := range permissionProfiles() {
		if p.ID == id {
			return p, nil
		}
	}
	return permissionProfile{}, errors.New("unknown permission profile; call amp_account_setup_read operation=profiles")
}
func stringList(v any) []string {
	items, _ := v.([]any)
	out := []string{}
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
func permissionLeaves(v any, out map[string]bool) {
	if items, ok := v.([]any); ok {
		for _, item := range items {
			permissionLeaves(item, out)
		}
		return
	}
	if m, ok := v.(map[string]any); ok {
		children, _ := m["Children"].([]any)
		if n, ok := m["Node"].(string); ok && len(children) == 0 {
			out[n] = true
		}
		permissionLeaves(children, out)
	}
}
func checkProfileNodes(ctx context.Context, cl *Client, scope string, nodes []string) error {
	v, e := cl.call(ctx, scope, "Core/GetPermissionsSpec", map[string]any{})
	if e != nil {
		return e
	}
	leaves := map[string]bool{}
	permissionLeaves(v, leaves)
	for _, node := range nodes {
		// AMP's supported all-nodes API grant. Its common-scope storage prefix
		// is managed by AMP; callers never edit RoleData or submit '*.*'.
		if node == "*" {
			continue
		}
		valid := leaves[node]
		// Instance-ID selectors and the ADS namespace grants resolved for full
		// admin must match a real advertised leaf. Operator grants stay exact.
		root, suffix, namespace := strings.Cut(node, ".")
		if strings.HasPrefix(node, "Instances.*.") || (namespace && suffix == "*" && apiIdentifier.MatchString(root)) {
			for leaf := range leaves {
				if ok, _ := path.Match(node, leaf); ok {
					valid = true
					break
				}
			}
		}
		if !valid {
			return fmt.Errorf("profile permission %s is not advertised in the selected context; no setup changes submitted", node)
		}
	}
	return nil
}
func requirePermissions(ctx context.Context, cl *Client, scope string, nodes []string) error {
	for _, node := range nodes {
		allowed, e := permissionHeld(ctx, cl, scope, node)
		if e != nil {
			return e
		}
		if !allowed {
			return fmt.Errorf("missing effective permission %s in the selected context", node)
		}
	}
	return nil
}
func advertisedNodes(ctx context.Context, cl *Client, scope string) ([]string, error) {
	v, e := cl.call(ctx, scope, "Core/GetPermissionsSpec", map[string]any{})
	if e != nil {
		return nil, e
	}
	leaves := map[string]bool{}
	permissionLeaves(v, leaves)
	if len(leaves) == 0 {
		return nil, errors.New("empty permission catalog")
	}
	nodes := []string{}
	for n := range leaves {
		if n != "*" && !strings.Contains(n, "*") {
			nodes = append(nodes, n)
		}
	}
	slices.Sort(nodes)
	return nodes, nil
}
func (a *App) planAccountSetup(ctx context.Context, x setupInput) (setupPlan, error) {
	p := setupPlan{Input: x}
	p.Input.PlanID = ""
	if x.Scope != "global" && x.Scope != "instance" {
		return p, errors.New("scope must be global or instance")
	}
	if len(x.Instances) == 0 || (x.Scope == "instance" && len(x.Instances) != 1) {
		return p, errors.New("instance scope requires exactly one application_instance_id; global scope needs one running context per module type")
	}
	var e error
	p.Profile, e = profileByID(x.Profile)
	if e != nil {
		return p, e
	}
	if p.Profile.FullScope && len(x.Instances) != 1 {
		return p, errors.New("full admin needs one application seed context; its shared grant covers all module types")
	}
	admin, e := a.client(x.Connection)
	if e != nil {
		return p, e
	}
	target, e := a.client(x.TargetConnection)
	if e != nil {
		return p, e
	}
	if strings.TrimRight(admin.conn.URL, "/") != strings.TrimRight(target.conn.URL, "/") {
		return p, errors.New("bootstrap and target must use the same ADS URL")
	}
	p.BootstrapTarget, p.Target = targetFingerprint(admin.conn), targetFingerprint(target.conn)
	rootInfo, e := admin.call(ctx, "", "Core/GetModuleInfo", map[string]any{})
	if e != nil {
		return p, e
	}
	rootMap, _ := rootInfo.(map[string]any)
	p.ADSInstanceID, _ = rootMap["InstanceId"].(string)
	if !validSetupGUID(p.ADSInstanceID) {
		return p, errors.New("ADS did not advertise its instance identity")
	}
	needed := []string{"Core.RoleManagement.ViewRoles", "Core.RoleManagement.EditRolePermissions", "Core.UserManagement.ViewUserInfo"}
	if x.Scope == "global" {
		needed = append(needed, "Core.RoleManagement.CreateRole", "Core.RoleManagement.CreateCommonRoles", "Core.UserManagement.ChangeRoleMembership")
	}
	if e = requirePermissions(ctx, admin, "", needed); e != nil {
		return p, e
	}
	if p.Profile.FullScope && x.Scope == "global" {
		if e = requirePermissions(ctx, admin, "", []string{"*"}); e != nil {
			return p, fmt.Errorf("full admin requires an already authorized full-scope bootstrap administrator, not an enumerated permission snapshot: %w; no setup changes submitted", e)
		}
	}
	instances, e := admin.call(ctx, "", "ADSModule/GetInstances", map[string]any{"ForceIncludeSelf": true})
	if e != nil {
		return p, e
	}
	for _, item := range flatten(instances) {
		if m, ok := item.(map[string]any); ok {
			if id, ok := m["InstanceID"].(string); ok {
				p.ExistingInstances = append(p.ExistingInstances, id)
			}
		}
	}
	slices.Sort(p.ExistingInstances)
	selector := "*"
	if x.Scope == "instance" {
		selector = x.Instances[0]
	}
	ads := []string{"Instances." + selector + ".Manage"}
	if x.Scope == "global" && p.Profile.FullScope {
		ads, e = adsAdminGrants(ctx, admin, "")
		if e != nil {
			return p, e
		}
	}
	slices.Sort(ads)
	subject := "new-role"
	if x.Scope == "instance" {
		subject = "existing-personal-role"
	}
	p.Grants = append(p.Grants, scopeGrant{Nodes: ads, Subject: subject})
	if e = checkProfileNodes(ctx, admin, "", ads); e != nil {
		return p, e
	}
	if e = requirePermissions(ctx, admin, "", ads); e != nil {
		return p, fmt.Errorf("bootstrap lacks the authority required by this profile: %w; no setup changes submitted", e)
	}
	modules := map[string]bool{}
	for _, scope := range x.Instances {
		if !validSetupGUID(scope) {
			return p, errors.New("invalid application instance GUID")
		}
		contextRights := []string{"Core.RoleManagement.EditRolePermissions"}
		if x.Scope == "instance" {
			contextRights = append(contextRights, "Core.RoleManagement.CreateRole", "Core.RoleManagement.ViewRoles", "Core.UserManagement.ChangeRoleMembership")
		}
		if e = requirePermissions(ctx, admin, scope, contextRights); e != nil {
			return p, e
		}
		info, e := admin.call(ctx, scope, "Core/GetModuleInfo", map[string]any{})
		if e != nil {
			return p, e
		}
		m, _ := info.(map[string]any)
		module, _ := m["ModuleName"].(string)
		if module == "" || strings.Contains(strings.ToLower(module), "ads") {
			return p, errors.New("select application contexts, not ADS")
		}
		if modules[module] {
			return p, errors.New("supply only one seed instance per module type")
		}
		modules[module] = true
		nodes, e := applicationProfileNodes(ctx, admin, scope, module, p.Profile)
		if e != nil {
			return p, e
		}
		if e = checkProfileNodes(ctx, admin, scope, nodes); e != nil {
			return p, e
		}
		if e = requirePermissions(ctx, admin, scope, nodes); e != nil {
			return p, fmt.Errorf("bootstrap lacks the authority required by this profile: %w; no setup changes submitted", e)
		}
		p.Grants = append(p.Grants, scopeGrant{Instance: scope, Module: module, Nodes: nodes, Subject: "new-role"})
	}
	users, e := admin.call(ctx, "", "Core/GetAMPUsersSummary", map[string]any{})
	if e != nil {
		return p, e
	}
	arr, _ := users.([]any)
	for _, u := range arr {
		if m, ok := u.(map[string]any); ok && m["Username"] == target.conn.Username && m["Disabled"] != true && m["IsLDAPUser"] != true && m["IsOIDCUser"] != true {
			p.UserID, _ = m["ID"].(string)
		}
	}
	if !validSetupGUID(p.UserID) {
		return p, errors.New("target must be an existing enabled local AMP account")
	}
	ui, e := admin.call(ctx, "", "Core/GetAMPUserInfo", map[string]any{"Username": target.conn.Username})
	if e != nil {
		return p, e
	}
	um, _ := ui.(map[string]any)
	p.Roles = stringList(um["Roles"])
	if x.BootstrapRole != "" && !slices.Contains(p.Roles, x.BootstrapRole) {
		return p, errors.New("temporary bootstrap role is not assigned to the target")
	}
	for _, role := range p.Roles {
		if role != p.UserID && role != x.BootstrapRole {
			return p, errors.New("use a dedicated target account with no other assigned roles, except its one named temporary bootstrap role")
		}
	}
	roleScope := ""
	if x.Scope == "instance" {
		roleScope = x.Instances[0]
	}
	roles, e := admin.call(ctx, roleScope, "Core/GetRoleData", map[string]any{})
	if e != nil {
		return p, e
	}
	rr, _ := roles.([]any)
	for _, r := range rr {
		if m, ok := r.(map[string]any); ok {
			if strings.EqualFold(fmt.Sprint(m["Name"]), x.RoleName) {
				return p, errors.New("role name already exists; inspect prior partial setup instead of replaying")
			}
			if m["ID"] == x.BootstrapRole && (m["DisableEdits"] == true || m["IsDefault"] == true || m["Hidden"] == true) {
				return p, errors.New("temporary bootstrap role must not be protected/default")
			}
		}
	}
	fresh, e := newClient(target.conn)
	if e != nil {
		return p, e
	}
	defer fresh.close()
	if _, e = fresh.call(ctx, "", "Core/GetModuleInfo", map[string]any{}); e != nil {
		return p, fmt.Errorf("target service login failed: %w", e)
	}
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	p.PlanID = hex.EncodeToString(h[:])
	return p, nil
}
func setupDir() string { return filepath.Join(filepath.Dir(configPath()), "setups") }
func saveSetup(r setupReceipt) error {
	return writeJSONAtomic(filepath.Join(setupDir(), r.ID+".json"), r)
}
func loadSetup(id string) (setupReceipt, error) {
	var r setupReceipt
	if len(id) != 32 {
		return r, errors.New("invalid setup_id")
	}
	if _, e := hex.DecodeString(id); e != nil {
		return r, errors.New("invalid setup_id")
	}
	b, e := os.ReadFile(filepath.Join(setupDir(), id+".json"))
	if e != nil {
		return r, errors.New("setup receipt not found")
	}
	e = json.Unmarshal(b, &r)
	if e == nil && r.ID != id {
		e = errors.New("setup receipt identity mismatch")
	}
	if e == nil && (len(r.Plan.Grants) < 2 || len(r.Plan.Input.Instances) == 0 || (r.Plan.Input.Scope != "global" && r.Plan.Input.Scope != "instance")) {
		e = errors.New("invalid or incomplete setup receipt; inspect its jobs before further changes")
	}
	return r, e
}
func setupLock() (func(), error) {
	if e := os.MkdirAll(setupDir(), 0700); e != nil {
		return nil, e
	}
	unlock, e := lockFile(filepath.Join(setupDir(), "setup.lock"))
	if e != nil {
		return nil, errors.New("another account setup is active; wait and inspect its receipt")
	}
	return unlock, nil
}
func setupMutation(ctx context.Context, cl *Client, r *setupReceipt, scope, endpoint string, args map[string]any) (any, error) {
	r.Step = endpoint
	toolName := "amp_setup_account"
	if r.Operation == "expand" {
		toolName = "amp_expand_privileges"
	}
	j, e := beginJob(Definition{Name: toolName, Endpoint: endpoint}, r.Plan.Input.Connection, cl.conn, scope, "")
	if e != nil {
		return nil, e
	}
	r.Jobs = append(r.Jobs, j.ID)
	if e = saveSetup(*r); e != nil {
		return nil, e
	}
	v, e := cl.call(ctx, scope, endpoint, args)
	_, e = finishSubmission(j, v, e)
	if e != nil {
		return nil, e
	}
	m, ok := v.(map[string]any)
	if !ok || m["Status"] != true {
		return nil, errors.New("setup mutation did not explicitly confirm success; reconcile the receipt before further changes")
	}
	return v, nil
}
func setupFailure(r *setupReceipt, e error) error {
	r.State = "needs_reconciliation"
	_ = saveSetup(*r)
	if jobs, err := readJobs(); err == nil {
		if j, ok := jobs[r.ID]; ok {
			j.State = "unknown"
			j.Note = "partial account setup; inspect setup receipt and child jobs before further writes"
			_ = saveJob(j)
		}
	}
	return fmt.Errorf("setup_id=%s role_id=%s step=%s: %w; partial changes may exist; inspect receipt/jobs, do not replay setup", r.ID, r.RoleID, r.Step, e)
}
func (a *App) applyAccountSetup(ctx context.Context, x setupInput) (any, error) {
	unlock, e := setupLock()
	if e != nil {
		return nil, e
	}
	defer unlock()
	p, e := a.planAccountSetup(ctx, x)
	if e != nil {
		return nil, e
	}
	if x.PlanID != p.PlanID {
		return nil, errors.New("setup plan is stale; call amp_account_setup_read operation=preview again")
	}
	admin, e := a.client(x.Connection)
	if e != nil {
		return nil, e
	}
	j, e := beginJob(Definition{Name: "amp_setup_account", Endpoint: "account-setup"}, x.Connection, admin.conn, "", "")
	if e != nil {
		return nil, e
	}
	r := setupReceipt{ID: j.ID, Plan: p, State: "applying", Jobs: []string{j.ID}}
	if e = saveSetup(r); e != nil {
		return nil, e
	}
	roleScope := ""
	if x.Scope == "instance" {
		roleScope = x.Instances[0]
	}
	v, e := setupMutation(ctx, admin, &r, roleScope, "Core/CreateRole", map[string]any{"Name": x.RoleName, "AsCommonRole": x.Scope == "global"})
	if e != nil {
		return nil, setupFailure(&r, e)
	}
	m, _ := v.(map[string]any)
	r.RoleID, _ = m["Result"].(string)
	if !validSetupGUID(r.RoleID) {
		return nil, setupFailure(&r, errors.New("CreateRole returned no role GUID"))
	}
	if e = saveSetup(r); e != nil {
		return nil, setupFailure(&r, e)
	}
	for _, group := range p.Grants {
		roleID := r.RoleID
		if group.Subject == "existing-personal-role" {
			roleID = p.UserID
		}
		for _, node := range group.Nodes {
			if _, e = setupMutation(ctx, admin, &r, group.Instance, "Core/SetAMPRolePermission", map[string]any{"RoleId": roleID, "PermissionNode": node, "Enabled": true}); e != nil {
				return nil, setupFailure(&r, e)
			}
		}
	}
	if _, e = setupMutation(ctx, admin, &r, roleScope, "Core/SetAMPUserRoleMembership", map[string]any{"UserId": p.UserID, "RoleId": r.RoleID, "IsMember": true}); e != nil {
		return nil, setupFailure(&r, e)
	}
	if e = a.verifySetup(ctx, r, x.Instances, false); e != nil {
		return nil, setupFailure(&r, e)
	}
	r.State = "configured"
	r.Step = "verify access and finalize; global scope requires a new instance per configured module"
	if e = saveSetup(r); e != nil {
		return nil, setupFailure(&r, e)
	}
	j.State = "succeeded"
	j.Note = "profile configured; separate fresh-instance verification and finalization required"
	if e = saveJob(j); e != nil {
		return nil, setupFailure(&r, e)
	}
	return setupSummary(r), nil
}
func validSetupGUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	b, e := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return e == nil && len(b) == 16
}
func (a *App) verifySetup(ctx context.Context, r setupReceipt, scopes []string, restricted bool) error {
	cl, e := a.client(r.Plan.Input.TargetConnection)
	if e != nil {
		return e
	}
	if targetFingerprint(cl.conn) != r.Plan.Target {
		return errors.New("target connection changed")
	}
	fresh, e := newClient(cl.conn)
	if e != nil {
		return e
	}
	defer fresh.close()
	if e = requirePermissions(ctx, fresh, "", r.Plan.Grants[0].Nodes); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if r.Plan.Input.Scope == "instance" && scope != r.Plan.Input.Instances[0] {
			return errors.New("local verification must select the configured instance")
		}
		info, e := fresh.call(ctx, scope, "Core/GetModuleInfo", map[string]any{})
		if e != nil {
			return e
		}
		m, _ := info.(map[string]any)
		module, _ := m["ModuleName"].(string)
		var grant *scopeGrant
		for i := range r.Plan.Grants {
			if (r.Plan.Grants[i].Module == module || (r.Plan.Profile.FullScope && r.Plan.Input.Scope == "global")) && r.Plan.Grants[i].Instance != "" {
				grant = &r.Plan.Grants[i]
				break
			}
		}
		if grant == nil || seen[module] {
			return errors.New("verification contexts must cover each configured module exactly once")
		}
		seen[module] = true
		if e = requirePermissions(ctx, fresh, scope, grant.Nodes); e != nil {
			return e
		}
		status, e := fresh.call(ctx, scope, "Core/GetStatus", map[string]any{})
		if e != nil {
			return e
		}
		if _, ok := status.(map[string]any); !ok {
			return errors.New("target did not receive a status object")
		}
		if _, e = fresh.call(ctx, scope, "Core/GetUpdates", map[string]any{}); e != nil {
			return e
		}
		if restricted && !r.Plan.Profile.FullScope {
			denied := []string{"Core.RoleManagement.EditRolePermissions", "Core.RoleManagement.CreateRole", "Core.UserManagement.ChangeRoleMembership", "Core.UserManagement.CreateNewUser", "Core.UserManagement.ResetUserPassword", "FileManager.FileManager.ModifyAMPConfigFiles", "Core.AppManagement.SendConsoleInput", "FileManager.FileManager.UploadFiles", "Core.AppManagement.StartApplication", "Core.AppManagement.StopApplication"}
			catalog, e := advertisedNodes(ctx, fresh, scope)
			if e != nil {
				return e
			}
			for _, node := range catalog {
				if operatorAdministrativeNode(node) {
					denied = append(denied, node)
				}
			}
			slices.Sort(denied)
			for _, node := range slices.Compact(denied) {
				if slices.Contains(grant.Nodes, node) {
					continue
				}
				v, e := fresh.call(ctx, scope, "Core/CurrentSessionHasPermission", map[string]any{"PermissionNode": node})
				if e != nil {
					return e
				}
				if v != false {
					return fmt.Errorf("unexpected effective permission: %s", node)
				}
			}
		}
	}
	if len(seen) != len(r.Plan.Grants)-1 {
		return errors.New("verification must cover every configured module")
	}
	if restricted && (r.Plan.Input.Scope == "instance" || !r.Plan.Profile.FullScope) {
		for _, node := range []string{"Core.RoleManagement.EditRolePermissions", "Core.UserManagement.ChangeRoleMembership", "ADS.InstanceManagement.CreateInstance"} {
			v, e := fresh.call(ctx, "", "Core/CurrentSessionHasPermission", map[string]any{"PermissionNode": node})
			if e != nil {
				return e
			}
			if v != false {
				return fmt.Errorf("unexpected ADS administration permission: %s", node)
			}
		}
	}
	if restricted && r.Plan.Input.Scope == "instance" {
		if len(r.Plan.ExistingInstances) < 2 {
			return errors.New("need a second instance to verify local access isolation")
		}
		for _, other := range r.Plan.ExistingInstances {
			if other == r.Plan.Input.Instances[0] || other == r.Plan.ADSInstanceID {
				continue
			}
			v, e := fresh.call(ctx, "", "Core/CurrentSessionHasPermission", map[string]any{"PermissionNode": "Instances." + other + ".Manage"})
			if e != nil {
				return e
			}
			if v != false {
				return errors.New("local target can manage another instance")
			}
		}
	}
	return nil
}
func (a *App) finalizeAccountSetup(ctx context.Context, connection, id string, scopes []string) (any, error) {
	unlock, e := setupLock()
	if e != nil {
		return nil, e
	}
	defer unlock()
	r, e := loadSetup(id)
	if e != nil {
		return nil, e
	}
	if connection != r.Plan.Input.Connection {
		return nil, errors.New("use the original bootstrap connection")
	}
	admin, e := a.client(connection)
	if e != nil {
		return nil, e
	}
	if targetFingerprint(admin.conn) != r.Plan.BootstrapTarget {
		return nil, errors.New("bootstrap connection changed")
	}
	if r.State == "complete" {
		return setupSummary(r), nil
	}
	if r.State != "configured" {
		return nil, errors.New("setup is not ready; inspect its receipt/jobs instead of replaying mutations")
	}
	if r.Plan.Input.Scope == "global" {
		for _, scope := range scopes {
			if slices.Contains(r.Plan.ExistingInstances, scope) {
				return nil, errors.New("global finalization needs newly created verification instances, not existing seed instances")
			}
		}
	}
	if e = a.verifySetup(ctx, r, scopes, false); e != nil {
		return nil, e
	}
	r.State = "finalizing"
	r.VerificationInstances = scopes
	if e = saveSetup(r); e != nil {
		return nil, e
	}
	if r.Plan.Input.BootstrapRole != "" {
		if _, e = setupMutation(ctx, admin, &r, "", "Core/SetAMPUserRoleMembership", map[string]any{"UserId": r.Plan.UserID, "RoleId": r.Plan.Input.BootstrapRole, "IsMember": false}); e != nil {
			return nil, setupFailure(&r, e)
		}
	}
	if e = a.verifySetup(ctx, r, scopes, true); e != nil {
		return nil, setupFailure(&r, e)
	}
	r.State = "complete"
	r.Step = "fresh-session access and scope checks passed"
	if e = saveSetup(r); e != nil {
		return nil, setupFailure(&r, e)
	}
	return setupSummary(r), nil
}
func setupSummary(r setupReceipt) any {
	return map[string]any{"setup_id": r.ID, "role_id": r.RoleID, "state": r.State, "step": r.Step, "profile": r.Plan.Input.Profile, "scope": r.Plan.Input.Scope, "jobs": r.Jobs}
}
func addAccountSetupTools(s registry, a *App) {
	empty := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	addTool(s, &mcp.Tool{Name: "amp_list_permission_profiles", InputSchema: empty, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return result(permissionProfiles(), nil), nil
	})
	instanceArray := map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": guidPattern}}
	for _, apply := range []bool{false, true} {
		props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1, "description": "Bootstrap administrator connection."}, "target_connection": map[string]any{"type": "string", "minLength": 1, "description": "Dedicated existing account service login on the same ADS."}, "profile": map[string]any{"type": "string", "enum": []string{"observer", "operator", "admin"}}, "scope": map[string]any{"type": "string", "enum": []string{"global", "instance"}}, "role_name": map[string]any{"type": "string", "minLength": 1, "maxLength": 100, "pattern": ".*\\S.*"}, "application_instance_ids": instanceArray, "bootstrap_role_id": map[string]any{"type": "string", "pattern": guidPattern, "description": "Optional dedicated temporary role removed from the target only during finalization."}}
		required := []string{"connection", "target_connection", "profile", "scope", "role_name", "application_instance_ids"}
		name := "amp_preview_account_setup"
		if apply {
			name = "amp_setup_account"
			props["plan_id"] = map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}
			required = append(required, "plan_id")
		}
		addTool(s, &mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: !apply}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var x setupInput
			json.Unmarshal(r.Params.Arguments, &x)
			if apply {
				v, e := a.applyAccountSetup(ctx, x)
				return result(v, e), nil
			}
			v, e := a.planAccountSetup(ctx, x)
			return result(v, e), nil
		})
	}
	addTool(s, &mcp.Tool{Name: "amp_finalize_account_setup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "setup_id": map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$"}, "verification_instance_ids": instanceArray}, "required": []string{"connection", "setup_id", "verification_instance_ids"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			ID         string   `json:"setup_id"`
			Scopes     []string `json:"verification_instance_ids"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		v, e := a.finalizeAccountSetup(ctx, x.Connection, x.ID, x.Scopes)
		return result(v, e), nil
	})
	addTool(s, &mcp.Tool{Name: "amp_get_account_setup", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"setup_id": map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$"}}, "required": []string{"setup_id"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			ID string `json:"setup_id"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		v, e := loadSetup(x.ID)
		return result(v, e), nil
	})
}
