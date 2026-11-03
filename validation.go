package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// internalTool is one AMP operation implementation. Internal tools are never
// published directly; facade.go exposes them through grouped public tools.
type internalTool struct {
	tool    *mcp.Tool
	schema  *jsonschema.Resolved
	handler mcp.ToolHandler
}
type registry map[string]internalTool

func resolveSchema(name string, schema any) *jsonschema.Resolved {
	b, e := json.Marshal(schema)
	if e != nil {
		panic(fmt.Errorf("tool %s schema: %w", name, e))
	}
	var s jsonschema.Schema
	if e = json.Unmarshal(b, &s); e != nil {
		panic(fmt.Errorf("tool %s schema: %w", name, e))
	}
	resolved, e := s.Resolve(nil)
	if e != nil {
		panic(fmt.Errorf("tool %s schema: %w", name, e))
	}
	return resolved
}

func addTool(s registry, t *mcp.Tool, h mcp.ToolHandler) {
	if _, dup := s[t.Name]; dup {
		panic("duplicate internal tool " + t.Name)
	}
	s[t.Name] = internalTool{tool: t, schema: resolveSchema(t.Name, t.InputSchema), handler: h}
}

// The SDK's raw AddTool intentionally does not validate arguments.
// Keep one explicit validation boundary for every published tool.
func publish(s *mcp.Server, t *mcp.Tool, h func(context.Context, map[string]any) *mcp.CallToolResult) {
	resolved := resolveSchema(t.Name, t.InputSchema)
	s.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := r.Params.Arguments
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		var args map[string]any
		if json.Unmarshal(raw, &args) != nil || args == nil {
			return result(nil, fmt.Errorf("invalid arguments for %s: expected a JSON object", t.Name)), nil
		}
		if e := resolved.Validate(args); e != nil {
			return result(nil, fmt.Errorf("invalid arguments for %s: %v; see its input schema", t.Name, e)), nil
		}
		return h(ctx, args), nil
	})
}

func (it internalTool) readOnly() bool {
	return it.tool.Annotations != nil && it.tool.Annotations.ReadOnlyHint
}
