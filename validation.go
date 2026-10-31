package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK's raw AddTool intentionally does not validate arguments.
// Keep one explicit validation boundary for all registered tools.
func addTool(s *mcp.Server, t *mcp.Tool, h mcp.ToolHandler) {
	b, e := json.Marshal(t.InputSchema)
	if e != nil {
		panic(fmt.Errorf("tool %s schema: %w", t.Name, e))
	}
	var schema jsonschema.Schema
	if e = json.Unmarshal(b, &schema); e != nil {
		panic(fmt.Errorf("tool %s schema: %w", t.Name, e))
	}
	resolved, e := schema.Resolve(nil)
	if e != nil {
		panic(fmt.Errorf("tool %s schema: %w", t.Name, e))
	}
	s.AddTool(t, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := r.Params.Arguments
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		var args any
		if json.Unmarshal(raw, &args) != nil {
			return result(nil, fmt.Errorf("invalid arguments for %s: expected a JSON object", t.Name)), nil
		}
		if e := resolved.Validate(args); e != nil {
			return result(nil, fmt.Errorf("invalid arguments for %s; use its required fields, types, bounds and enum choices from tools/list", t.Name)), nil
		}
		return h(ctx, r)
	})
}
