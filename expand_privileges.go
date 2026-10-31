package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type expandInput struct {
	Connection string `json:"connection"`
	SetupID    string `json:"setup_id"`
	Instance   string `json:"instance_id"`
	Module     string `json:"module"`
	Mode       string `json:"mode"`
	PlanID     string `json:"plan_id,omitempty"`
}
type expansionPlan struct {
	SetupID      string   `json:"setup_id"`
	RoleID       string   `json:"role_id"`
	Profile      string   `json:"profile"`
	Scope        string   `json:"scope"`
	Instance     string   `json:"instance_id"`
	Module       string   `json:"module"`
	Missing      []string `json:"missing_permissions"`
	MissingActor []string `json:"missing_actor_permissions"`
	ClearDenials []string `json:"denials_to_clear"`
	PlanID       string   `json:"plan_id"`
	Desired      []string `json:"-"`
}

// AMP ACLs use '*' wildcards and '-' denials. More-specific entries win;
// denial wins a tie, matching AMP 2.8's shipped evaluatePermission routine.
func roleAllows(nodes []string, node string) bool {
	matched, allowed, highest := false, false, 0
	for _, raw := range nodes {
		negative := strings.HasPrefix(raw, "-")
		p := strings.TrimPrefix(raw, "-")
		spec := len(strings.Split(p, "."))
		if strings.HasSuffix(p, ".*") {
			spec--
		}
		pattern := "^" + strings.ReplaceAll(regexp.QuoteMeta(p), "\\*", ".*") + "$"
		if ok, _ := regexp.MatchString(pattern, node); !ok {
			continue
		}
		if !matched || spec > highest || (negative && spec == highest) {
			matched = true
			allowed = !negative
			highest = spec
		}
	}
	return matched && allowed
}

func (a *App) planPrivilegeExpansion(ctx context.Context, x expandInput) (expansionPlan, setupReceipt, error) {
	if x.SetupID == "" {
		cl, e := a.client(x.Connection)
		if e != nil {
			return expansionPlan{}, setupReceipt{}, e
		}
		paths, e := filepath.Glob(filepath.Join(setupDir(), "*.json"))
		if e != nil {
			return expansionPlan{}, setupReceipt{}, e
		}
		matches := []string{}
		for _, p := range paths {
			id := strings.TrimSuffix(filepath.Base(p), ".json")
			r, e := loadSetup(id)
			if e != nil || r.State != "complete" || r.Operation == "expand" || r.Plan.Target != targetFingerprint(cl.conn) {
				continue
			}
			if r.Plan.Input.Scope == "instance" && r.Plan.Input.Instances[0] != x.Instance {
				continue
			}
			matches = append(matches, id)
		}
		if len(matches) != 1 {
			return expansionPlan{}, setupReceipt{}, fmt.Errorf("found %d matching completed setups; supply the original setup_id explicitly", len(matches))
		}
		x.SetupID = matches[0]
	}
	p := expansionPlan{SetupID: x.SetupID, Instance: x.Instance, Module: x.Module}
	r, e := loadSetup(x.SetupID)
	if e != nil {
		return p, r, e
	}
	if r.State != "complete" || r.Operation == "expand" {
		return p, r, errors.New("use the completed ORIGINAL account setup_id, not a partial setup or expansion receipt")
	}
	profile, e := profileByID(r.Plan.Input.Profile)
	if e != nil {
		return p, r, e
	}
	if r.Plan.Input.Scope == "instance" && x.Instance != r.Plan.Input.Instances[0] {
		return p, r, errors.New("local profiles cannot expand to another instance")
	}
	cl, e := a.client(x.Connection)
	if e != nil {
		return p, r, e
	}
	target, e := a.client(r.Plan.Input.TargetConnection)
	if e != nil {
		return p, r, e
	}
	if targetFingerprint(target.conn) != r.Plan.Target {
		return p, r, errors.New("configured target account changed since setup")
	}
	if strings.TrimRight(cl.conn.URL, "/") != strings.TrimRight(target.conn.URL, "/") {
		return p, r, errors.New("expansion must use the same ADS URL as the original setup")
	}
	if e = requirePermissions(ctx, cl, x.Instance, []string{"Core.RoleManagement.ViewRoles", "Core.RoleManagement.EditRolePermissions"}); e != nil {
		return p, r, e
	}
	info, e := cl.call(ctx, x.Instance, "Core/GetModuleInfo", map[string]any{})
	if e != nil {
		return p, r, e
	}
	m, _ := info.(map[string]any)
	module, _ := m["ModuleName"].(string)
	if module == "" || strings.Contains(strings.ToLower(module), "ads") || x.Module != module {
		return p, r, errors.New("module must exactly match the running application instance's ModuleName; ADS is not an application expansion context")
	}
	roles, e := cl.call(ctx, x.Instance, "Core/GetRoleData", map[string]any{})
	if e != nil {
		return p, r, e
	}
	arr, _ := roles.([]any)
	var role map[string]any
	for _, v := range arr {
		if m, ok := v.(map[string]any); ok && m["ID"] == r.RoleID {
			role = m
			break
		}
	}
	if role == nil {
		return p, r, errors.New("managed role is absent from this application context")
	}
	if r.Plan.Input.Scope == "global" && role["IsCommonRole"] != true {
		return p, r, errors.New("managed global role is no longer a template role")
	}
	if r.Plan.Input.Scope == "instance" && role["IsInstanceSpecific"] != true {
		return p, r, errors.New("managed local role is no longer instance-specific")
	}
	member := false
	if members, ok := role["Members"].([]any); ok {
		for _, v := range members {
			if m, ok := v.(map[string]any); ok && m["ID"] == r.Plan.UserID {
				member = true
			}
		}
	}
	if !member {
		return p, r, errors.New("the original target account is no longer a member of the managed role")
	}
	p.RoleID, p.Profile, p.Scope = r.RoleID, profile.ID, r.Plan.Input.Scope
	p.Desired, e = applicationProfileNodes(ctx, cl, x.Instance, x.Module, profile)
	if e != nil {
		return p, r, e
	}
	if e = checkProfileNodes(ctx, cl, x.Instance, p.Desired); e != nil {
		return p, r, e
	}
	v, e := cl.call(ctx, x.Instance, "Core/GetAMPRolePermissions", map[string]any{"RoleId": r.RoleID})
	if e != nil {
		return p, r, e
	}
	if _, ok := v.([]any); !ok {
		return p, r, errors.New("unexpected role permission response")
	}
	current := stringList(v)
	p.Missing = []string{}
	p.ClearDenials = []string{}
	if profile.FullScope {
		for _, n := range current {
			if strings.HasPrefix(n, "-") {
				p.ClearDenials = append(p.ClearDenials, strings.TrimPrefix(n, "-"))
			}
		}
	}
	for _, node := range p.Desired {
		if !roleAllows(current, node) {
			p.Missing = append(p.Missing, node)
		}
	}
	p.MissingActor = []string{}
	actorNeeded := slices.Clone(p.Missing)
	if len(p.ClearDenials) > 0 && !slices.Contains(actorNeeded, "*") {
		actorNeeded = append(actorNeeded, "*")
	}
	for _, node := range actorNeeded {
		allowed, e := permissionHeld(ctx, cl, x.Instance, node)
		if e != nil {
			return p, r, e
		}
		if !allowed {
			p.MissingActor = append(p.MissingActor, node)
		}
	}
	// Bind the entire catalog and ACL snapshot, not just the delta. Changes to
	// the role, members, actor, profile or metadata require another preview.
	b, _ := json.Marshal(map[string]any{"plan": p, "desired": p.Desired, "current": current, "members": role["Members"], "actor": targetFingerprint(cl.conn), "target": r.Plan.Target, "profile": profile})
	h := sha256.Sum256(b)
	p.PlanID = hex.EncodeToString(h[:])
	return p, r, nil
}
func (a *App) expandPrivileges(ctx context.Context, x expandInput) (any, error) {
	if x.Mode != "preview" && x.Mode != "apply" {
		return nil, errors.New("mode must be preview or apply")
	}
	if x.Mode == "apply" && len(x.PlanID) != 64 {
		return nil, errors.New("apply requires plan_id returned by preview")
	}
	if x.Mode == "preview" {
		p, _, e := a.planPrivilegeExpansion(ctx, x)
		return p, e
	}
	unlock, e := setupLock()
	if e != nil {
		return nil, e
	}
	defer unlock()
	p, original, e := a.planPrivilegeExpansion(ctx, x)
	if e != nil {
		return nil, e
	}
	if p.PlanID != x.PlanID {
		return nil, errors.New("privilege expansion plan is stale; preview again before applying")
	}
	if len(p.MissingActor) > 0 {
		return nil, fmt.Errorf("acting account lacks %d requested permission(s), starting with %s; use an authorized bootstrap administrator connection that already has these rights and preview again; no expansion writes submitted", len(p.MissingActor), p.MissingActor[0])
	}
	cl, e := a.client(x.Connection)
	if e != nil {
		return nil, e
	}
	// Keep an independently readable receipt using the same journal/recovery
	// machinery. The original setup receipt and its scope remain immutable.
	r := original
	r.Operation = "expand"
	r.Plan.Profile, _ = profileByID(p.Profile)
	r.Plan.Input.Connection = x.Connection
	r.Plan.BootstrapTarget = targetFingerprint(cl.conn)
	r.Plan.Grants = []scopeGrant{original.Plan.Grants[0], {Instance: x.Instance, Module: p.Module, Nodes: p.Desired, Subject: "new-role"}}
	r.VerificationInstances = nil
	if len(p.Missing) == 0 && len(p.ClearDenials) == 0 {
		if e = a.verifySetup(ctx, r, []string{x.Instance}, false); e != nil {
			return nil, e
		}
		return map[string]any{"unchanged": true, "role_id": p.RoleID, "module": p.Module, "verified": true}, nil
	}
	j, e := beginJob(Definition{Name: "amp_expand_privileges", Endpoint: "privilege-expansion"}, x.Connection, cl.conn, x.Instance, x.Instance)
	if e != nil {
		return nil, e
	}
	r.ID, r.State, r.Step, r.Jobs = j.ID, "applying", "expanding profile permissions", []string{j.ID}
	if e = saveSetup(r); e != nil {
		return nil, e
	}
	for _, node := range p.ClearDenials {
		if _, e = setupMutation(ctx, cl, &r, x.Instance, "Core/SetAMPRolePermission", map[string]any{"RoleId": p.RoleID, "PermissionNode": node, "Enabled": nil}); e != nil {
			return nil, setupFailure(&r, e)
		}
	}
	for _, node := range p.Missing {
		if _, e = setupMutation(ctx, cl, &r, x.Instance, "Core/SetAMPRolePermission", map[string]any{"RoleId": p.RoleID, "PermissionNode": node, "Enabled": true}); e != nil {
			return nil, setupFailure(&r, e)
		}
	}
	if e = a.verifySetup(ctx, r, []string{x.Instance}, false); e != nil {
		return nil, setupFailure(&r, e)
	}
	r.State, r.Step = "complete", "expanded profile permissions verified with a fresh target session"
	if e = saveSetup(r); e != nil {
		return nil, setupFailure(&r, e)
	}
	j.State, j.Note = "succeeded", "profile-bound privilege expansion verified; receipt available through amp_get_account_setup"
	if e = saveJob(j); e != nil {
		return nil, setupFailure(&r, e)
	}
	return map[string]any{"expansion_id": r.ID, "role_id": p.RoleID, "module": p.Module, "added_permissions": p.Missing, "cleared_denials": p.ClearDenials, "state": r.State, "jobs": r.Jobs}, nil
}
func addExpandPrivilegesTool(s *mcp.Server, a *App) {
	props := map[string]any{
		"connection":  map[string]any{"type": "string", "minLength": 1, "description": "Authorized role administrator already possessing the rights it grants; use bootstrap if the target lacks them."},
		"setup_id":    map[string]any{"type": "string", "pattern": "^[a-f0-9]{32}$", "description": "Original completed setup receipt. Optional only with exactly one matching setup for the acting account; specify when using a different bootstrap account."},
		"instance_id": map[string]any{"type": "string", "pattern": guidPattern, "description": "Running example of the new module or application configuration."},
		"module":      map[string]any{"type": "string", "minLength": 1, "maxLength": 100, "description": "Exact ModuleName from amp_get_application_info; checked against instance_id."},
		"mode":        map[string]any{"type": "string", "enum": []string{"preview", "apply"}},
		"plan_id":     map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "description": "Required for apply; use the exact value returned by preview."},
	}
	addTool(s, &mcp.Tool{Name: "amp_expand_privileges", Description: "Initialize an existing managed role for a new module or application configuration. Preview returns missing profile rights and missing actor rights; apply requires plan_id and an actor holding the rights it grants. Admin ensures full application authority; operator resolves module settings/actions and game-file rights; observer keeps fixed read rights. Local roles cannot expand to another instance. Journals partial failures, verifies fresh target access and never replays writes. Does not add ADS rights, assign stronger memberships or change the profile.", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id", "module", "mode"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x expandInput
		if e := json.Unmarshal(r.Params.Arguments, &x); e != nil {
			return result(nil, fmt.Errorf("invalid expansion input")), nil
		}
		v, e := a.expandPrivileges(ctx, x)
		return result(v, e), nil
	})
}
