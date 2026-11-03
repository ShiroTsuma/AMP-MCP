package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sync"
)

type consoleBuffer struct {
	mu     sync.Mutex
	offset int
	lines  []any
}

var buffers sync.Map

func addExtraTools(s registry, a *App) {
	props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": "^[0-9a-fA-F-]{36}$"}, "cursor": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}
	props["mode"] = map[string]any{"type": "string", "enum": []string{"tail", "since"}, "description": "Without mode: newest entries when cursor is omitted, otherwise entries since cursor. since requires cursor; tail rejects cursor."}
	addTool(s, &mcp.Tool{Name: "amp_read_console", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string `json:"connection"`
			Instance   string `json:"instance_id"`
			Cursor     *int   `json:"cursor"`
			Mode       string `json:"mode"`
			Limit      int    `json:"limit"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		if x.Mode == "" {
			x.Mode = "tail"
			if x.Cursor != nil {
				x.Mode = "since"
			}
		}
		if (x.Mode == "tail" && x.Cursor != nil) || (x.Mode == "since" && x.Cursor == nil) {
			return result(nil, fmt.Errorf("tail omits cursor; since requires cursor from this process")), nil
		}
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		key := x.Connection + "/" + x.Instance
		v, _ := buffers.LoadOrStore(key, &consoleBuffer{lines: []any{}})
		b := v.(*consoleBuffer)
		b.mu.Lock()
		defer b.mu.Unlock()
		data, e := cl.call(ctx, x.Instance, "Core/GetUpdates", map[string]any{})
		if e != nil {
			return result(nil, e), nil
		}
		if m, ok := data.(map[string]any); ok {
			if lines, ok := m["ConsoleEntries"].([]any); ok {
				b.lines = append(b.lines, lines...)
			}
		}
		if len(b.lines) > 2000 {
			drop := len(b.lines) - 2000
			b.lines = b.lines[drop:]
			b.offset += drop
		}
		limit := x.Limit
		if limit == 0 {
			limit = 100
		}
		lost := false
		start := max(0, len(b.lines)-limit)
		if x.Mode == "since" {
			cursor := *x.Cursor
			lost = cursor < b.offset
			start = max(cursor-b.offset, 0)
		} else {
			lost = b.offset > 0 || start > 0
		}
		if start > len(b.lines) {
			return result(nil, fmt.Errorf("cursor is ahead of this process; restart from cursor 0")), nil
		}
		end := min(start+limit, len(b.lines))
		return result(map[string]any{"entries": b.lines[start:end], "next_cursor": b.offset + end, "truncated": lost, "history": "received_by_this_process", "mode": x.Mode}, nil), nil
	})
	schema := map[string]any{"type": "object", "properties": map[string]any{"connection": props["connection"], "instance_id": props["instance_id"]}, "required": []string{"connection"}, "additionalProperties": false}
	{
		addTool(s, &mcp.Tool{Name: "amp_test_connection", InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var x struct {
				Connection string `json:"connection"`
				Scope      string `json:"instance_id"`
			}
			json.Unmarshal(r.Params.Arguments, &x)
			cl, e := a.client(x.Connection)
			if e != nil {
				return result(nil, e), nil
			}
			v, e := cl.call(ctx, x.Scope, "Core/GetModuleInfo", map[string]any{})
			if e != nil {
				return result(nil, e), nil
			}
			out := map[string]any{}
			if m, ok := v.(map[string]any); ok {
				for _, k := range []string{"AMPVersion", "APIVersion", "ModuleName", "LoadedPlugins", "FeatureSet"} {
					out[k] = m[k]
				}
			}
			return result(out, nil), nil
		})
	}
}
