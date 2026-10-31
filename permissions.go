package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func adsAdminGrants(ctx context.Context, cl *Client, scope string) ([]string, error) {
	v, e := cl.call(ctx, scope, "Core/GetPermissionsSpec", map[string]any{})
	if e != nil {
		return nil, e
	}
	leaves := map[string]bool{}
	permissionLeaves(v, leaves)
	names := map[string]bool{}
	for node := range leaves {
		root, _, found := strings.Cut(node, ".")
		if found && apiIdentifier.MatchString(root) {
			names[root+".*"] = true
		}
	}
	out := []string{}
	for node := range names {
		out = append(out, node)
	}
	slices.Sort(out)
	if len(out) == 0 {
		return nil, errors.New("ADS did not advertise permission namespaces")
	}
	return out, nil
}

func fullScopeAuthority(ctx context.Context, cl *Client, scope string) (bool, error) {
	// CurrentSessionHasPermission accepts concrete nodes. A literal '*' query
	// can return false for a role whose scoped ACL correctly contains '*'.
	v, e := cl.call(ctx, scope, "Core/CurrentSessionHasPermission", map[string]any{"PermissionNode": "*"})
	if e != nil {
		return false, e
	}
	if v == true {
		return true, nil
	}
	info, e := cl.call(ctx, scope, "Core/GetModuleInfo", map[string]any{})
	if e != nil {
		return false, e
	}
	im, _ := info.(map[string]any)
	module, _ := im["ModuleName"].(string)
	if strings.Contains(strings.ToLower(module), "ads") {
		// Common application '*' is intentionally not ADS authority in AMP 2.8.
		// ADS uses namespace grants, e.g. Core.*, ADS.* and Settings.*.
		nodes, e := adsAdminGrants(ctx, cl, scope)
		if e != nil {
			return false, e
		}
		for _, node := range nodes {
			v, e = cl.call(ctx, scope, "Core/CurrentSessionHasPermission", map[string]any{"PermissionNode": node})
			if e != nil {
				return false, e
			}
			if v != true {
				return false, nil
			}
		}
		return true, nil
	}
	v, e = cl.call(ctx, scope, "Core/GetAMPUserInfo", map[string]any{"Username": cl.conn.Username})
	if e != nil {
		return false, e
	}
	m, ok := v.(map[string]any)
	if !ok {
		return false, errors.New("unexpected current-user role metadata")
	}
	roles := stringList(m["Roles"])
	if len(roles) == 0 {
		return false, nil
	}
	permissions := []string{}
	for _, id := range roles {
		v, e = cl.call(ctx, scope, "Core/GetAMPRolePermissions", map[string]any{"RoleId": id})
		if e != nil {
			return false, e
		}
		if _, ok := v.([]any); !ok {
			return false, errors.New("unexpected current-user ACL metadata")
		}
		permissions = append(permissions, stringList(v)...)
	}
	for _, p := range permissions {
		if strings.HasPrefix(p, "-") {
			return false, nil
		}
	}
	return roleAllows(permissions, "*"), nil
}
func permissionHeld(ctx context.Context, cl *Client, scope, node string) (bool, error) {
	if node == "*" {
		return fullScopeAuthority(ctx, cl, scope)
	}
	v, e := cl.call(ctx, scope, "Core/CurrentSessionHasPermission", map[string]any{"PermissionNode": node})
	if e != nil {
		return false, e
	}
	allowed, ok := v.(bool)
	if !ok {
		return false, errors.New("unexpected permission response")
	}
	return allowed, nil
}

func addPermissionTool(s *mcp.Server, a *App) {
	props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": guidPattern}, "permissions": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}}}
	addTool(s, &mcp.Tool{Name: "amp_check_permissions", Description: "Check the current authenticated session's effective AMP permissions for permission nodes. The special node * verifies full scope authority, including ADS namespace checks. Omit instance_id for ADS. Returns named booleans; does not change roles.", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "permissions"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection  string
			Scope       string `json:"instance_id"`
			Permissions []string
		}
		json.Unmarshal(r.Params.Arguments, &x)
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		out := map[string]bool{}
		for _, node := range x.Permissions {
			allowed, e := permissionHeld(ctx, cl, x.Scope, node)
			if e != nil {
				return result(nil, e), nil
			}
			out[node] = allowed
		}
		return result(out, nil), nil
	})
}
