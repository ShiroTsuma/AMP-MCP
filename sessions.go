package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"sync"
	"time"
)

type sessionReference struct {
	Connection, Scope, Target, ID string
	Expires                       time.Time
}

var sessionHandles struct {
	sync.Mutex
	items map[string]sessionReference
}

func addSessionTools(s *mcp.Server, a *App) {
	text := map[string]any{"type": "string", "minLength": 1}
	addTool(s, &mcp.Tool{Name: "amp_list_sessions", Description: "List AMP sessions without exposing remote session IDs. If AMP provides an Id, returns a process-local session_handle valid for 15 minutes; otherwise returns can_terminate=false. Omit instance_id for ADS.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"connection": text, "instance_id": map[string]any{"type": "string", "pattern": "^[0-9a-fA-F-]{36}$"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"connection"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection    string
			Scope         string `json:"instance_id"`
			Limit, Offset int
		}
		json.Unmarshal(r.Params.Arguments, &x)
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := cl.call(ctx, x.Scope, "Core/GetActiveAMPSessions", map[string]any{})
		if e != nil {
			return result(nil, e), nil
		}
		entries, ok := v.([]any)
		if !ok {
			return result(nil, errors.New("unexpected session list shape")), nil
		}
		if x.Limit == 0 {
			x.Limit = 50
		}
		start := min(x.Offset, len(entries))
		end := min(start+x.Limit, len(entries))
		out := []any{}
		sessionHandles.Lock()
		defer sessionHandles.Unlock()
		if sessionHandles.items == nil {
			sessionHandles.items = map[string]sessionReference{}
		}
		now := time.Now()
		for key, ref := range sessionHandles.items {
			if now.After(ref.Expires) {
				delete(sessionHandles.items, key)
			}
		}
		if len(sessionHandles.items)+(end-start) > 2048 {
			return result(nil, errors.New("session handle capacity reached; retry after old handles expire")), nil
		}
		for _, entry := range entries[start:end] {
			m, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["Id"].(string)
			item := selectKeys(m, "Username", "StartTime", "LastActivity", "Source", "SessionType", "IsCurrent")
			item["can_terminate"] = id != ""
			if id == "" {
				out = append(out, item)
				continue
			}
			var bytes [16]byte
			if _, e = rand.Read(bytes[:]); e != nil {
				return result(nil, e), nil
			}
			handle := hex.EncodeToString(bytes[:])
			sessionHandles.items[handle] = sessionReference{Connection: x.Connection, Scope: x.Scope, Target: targetFingerprint(cl.conn), ID: id, Expires: now.Add(15 * time.Minute)}
			item["session_handle"] = handle
			out = append(out, item)
		}
		return result(map[string]any{"items": out, "next_offset": end, "more": end < len(entries), "handle_lifetime_seconds": 900}, nil), nil
	})
	addTool(s, &mcp.Tool{Name: "amp_end_session", Description: "End an AMP session using a session_handle returned by amp_list_sessions in this MCP process. May log out that user's panel or API connection.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"connection": text, "session_handle": text}, "required": []string{"connection", "session_handle"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Handle     string `json:"session_handle"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		sessionHandles.Lock()
		ref, ok := sessionHandles.items[x.Handle]
		sessionHandles.Unlock()
		if !ok || ref.Connection != x.Connection || time.Now().After(ref.Expires) {
			return result(nil, errors.New("session handle expired or is unknown; call amp_list_sessions again")), nil
		}
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		if targetFingerprint(cl.conn) != ref.Target {
			return result(nil, errors.New("connection target changed; list sessions again")), nil
		}
		caps, e := cl.call(ctx, ref.Scope, "Core/GetAPISpec", map[string]any{})
		if e != nil {
			return result(nil, e), nil
		}
		if !supportsEndpoint(caps, "Core/EndUserSession") {
			return result(nil, errors.New("session termination is not supported by this AMP instance")), nil
		}
		j, e := beginJob(Definition{Name: "amp_end_session", Endpoint: "Core/EndUserSession"}, x.Connection, cl.conn, ref.Scope, ref.Scope)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := cl.call(ctx, ref.Scope, "Core/EndUserSession", map[string]any{"Id": ref.ID})
		if e == nil {
			sessionHandles.Lock()
			delete(sessionHandles.items, x.Handle)
			sessionHandles.Unlock()
		}
		v, e = finishSubmission(j, v, e)
		return result(v, e), nil
	})
}
