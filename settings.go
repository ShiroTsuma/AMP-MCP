package main

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
)

var secretNode = regexp.MustCompile(`(?i)(password|passwd|secret|token|api.?key|private.?key|licen[cs]e.?key|credential|connection.?string)`)

func sensitiveSetting(m map[string]any) bool {
	node, _ := m["Node"].(string)
	input, _ := m["InputType"].(string)
	return secretNode.MatchString(node) || strings.EqualFold(input, "password") || strings.EqualFold(input, "secret") || m["IsSecret"] == true || m["Sensitive"] == true
}
func checkSetting(ctx context.Context, c *Client, scope, node string, write bool) error {
	v, e := c.call(ctx, scope, "Core/GetConfig", map[string]any{"node": node})
	if e != nil {
		return e
	}
	m, ok := v.(map[string]any)
	if !ok || m["Node"] != node {
		return errors.New("setting was not found in this instance")
	}
	if sensitiveSetting(m) {
		return errors.New("secret settings must be managed locally in the AMP panel")
	}
	if write && m["ReadOnly"] == true {
		return errors.New("setting is read-only in AMP")
	}
	return nil
}
func selectKeys(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}
func safeSettingsMetadata(v any, args map[string]any) (any, error) {
	groups, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("unexpected settings specification shape")
	}
	items := []any{}
	section, _ := args["section"].(string)
	query, _ := args["query"].(string)
	query = strings.ToLower(query)
	for name, group := range groups {
		if section != "" && !strings.EqualFold(name, section) {
			continue
		}
		if entries, ok := group.([]any); ok {
			for _, entry := range entries {
				if m, ok := entry.(map[string]any); ok {
					if query != "" {
						matched := false
						for _, field := range []string{"Node", "Name", "Description", "Category", "Subcategory", "Keywords"} {
							if text, ok := m[field].(string); ok && strings.Contains(strings.ToLower(text), query) {
								matched = true
								break
							}
						}
						if !matched {
							continue
						}
					}
					item := selectKeys(m, "Node", "Name", "Description", "Category", "Subcategory", "InputType", "ValType", "ReadOnly", "RequiresRestart", "Required", "MinValue", "MaxValue", "MaxLength")
					item["section"] = name
					item["sensitive"] = sensitiveSetting(m)
					items = append(items, item)
				}
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i].(map[string]any)["Node"].(string)
		b, _ := items[j].(map[string]any)["Node"].(string)
		return a < b
	})
	pageArgs := map[string]any{}
	for k, v := range args {
		if k != "query" {
			pageArgs[k] = v
		}
	}
	return pageItems(items, pageArgs)
}
