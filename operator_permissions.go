package main

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Game/module authority must not become AMP security or panel authority.
func operatorAdministrativeNode(node string) bool {
	for _, prefix := range []string{"Core.RoleManagement.", "Core.UserManagement.", "Core.Special.", "Settings.Core.", "Settings.ADSModule.", "ADS.", "Instances."} {
		if strings.HasPrefix(node, prefix) {
			return true
		}
	}
	return node == "FileManager.FileManager.ModifyAMPConfigFiles"
}

func operatorModuleNode(module, node string) bool {
	if operatorAdministrativeNode(node) || !apiIdentifier.MatchString(module) || !isActionModule(module) {
		return false
	}
	for _, prefix := range []string{"Settings." + module + ".", "Settings.Meta." + module + ".", module + "."} {
		if strings.HasPrefix(node, prefix) {
			return true
		}
	}
	// AMP's module permission namespaces can omit the Module suffix.
	name := strings.TrimSuffix(module, "Module")
	return name != "" && strings.HasPrefix(node, name+".")
}

func applicationProfileNodes(ctx context.Context, cl *Client, scope, module string, profile permissionProfile) ([]string, error) {
	if profile.FullScope {
		return []string{"*"}, nil
	}
	nodes := slices.Clone(profile.Application)
	if !profile.ModuleManagement {
		return nodes, nil
	}
	if !isActionModule(module) {
		return nil, errors.New("operator requires an application module, not an AMP administration context")
	}
	catalog, e := advertisedNodes(ctx, cl, scope)
	if e != nil {
		return nil, e
	}
	spec, e := cl.apiSpec(ctx, scope, true)
	if e != nil {
		return nil, e
	}
	api, _ := spec.(map[string]any)
	methods, _ := api[module].(map[string]any)
	actionPermissions := []string{}
	for _, method := range methods {
		metadata, _ := method.(map[string]any)
		actionPermissions = append(actionPermissions, stringList(metadata["RequiredPermissions"])...)
	}
	for _, node := range catalog {
		// A module API can use a differently named game permission namespace.
		// Core, Settings and FileManager stay subject to the explicit policy.
		root, _, _ := strings.Cut(node, ".")
		action := root != "Core" && root != "Settings" && root != "FileManager" && roleAllows(actionPermissions, node)
		if !operatorAdministrativeNode(node) && (strings.HasPrefix(node, "FileManager.FileManager.") || operatorModuleNode(module, node) || action) {
			nodes = append(nodes, node)
		}
	}
	// Use exact advertised leaves. Future modules/settings require an explicit
	// expansion preview; broad Settings.* or FileManager.* could grant AMP access.
	slices.Sort(nodes)
	return slices.Compact(nodes), nil
}
