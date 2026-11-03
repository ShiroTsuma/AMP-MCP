package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addSettingSecretTool(s registry, a *App) {
	props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": guidPattern}, "node": map[string]any{"type": "string", "minLength": 1, "maxLength": 512}, "overwrite": map[string]any{"type": "boolean", "description": "Explicitly allow replacing a configured secret."}}
	addTool(s, &mcp.Tool{Name: "amp_generate_setting_secret", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "node", "overwrite"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Scope      string `json:"instance_id"`
			Node       string
			Overwrite  bool
		}
		json.Unmarshal(r.Params.Arguments, &x)
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := cl.call(ctx, x.Scope, "Core/GetConfig", map[string]any{"node": x.Node})
		if e != nil {
			return result(nil, e), nil
		}
		m, ok := v.(map[string]any)
		if !ok || m["Node"] != x.Node || !sensitiveSetting(m) || m["ReadOnly"] == true {
			return result(nil, errors.New("node must be an existing writable secret setting")), nil
		}
		if !x.Overwrite {
			if value := m["CurrentValue"]; value != nil && value != "" {
				return result(nil, errors.New("secret appears configured; explicit overwrite=true is required")), nil
			}
		}
		if e = requirePermissions(ctx, cl, x.Scope, []string{"Settings." + x.Node}); e != nil {
			return result(nil, e), nil
		}
		buf := make([]byte, 24)
		if _, e = rand.Read(buf); e != nil {
			return result(nil, e), nil
		}
		defer clear(buf)
		secret := "Aa1!" + base64.RawURLEncoding.EncodeToString(buf)
		cipher, e := encryptCredential(secret)
		if e != nil {
			return result(nil, e), nil
		}
		dir := filepath.Join(filepath.Dir(configPath()), "credentials")
		if e = os.MkdirAll(dir, 0700); e != nil {
			return result(nil, errors.New("cannot create credential directory")), nil
		}
		f, e := os.CreateTemp(dir, "setting-*.json")
		if e != nil {
			return result(nil, errors.New("cannot create encrypted credential file")), nil
		}
		name := f.Name()
		f.Close()
		saved := map[string]any{"version": 1, "kind": "setting-secret", "connection": x.Connection, "instance_id": x.Scope, "target": targetFingerprint(cl.conn), "username": "Setting: " + x.Node, "node": x.Node, "passwordDpapi": cipher}
		if e = writeJSONAtomic(name, saved); e != nil {
			os.Remove(name)
			return result(nil, errors.New("cannot save encrypted setting credential; no write submitted")), nil
		}
		j, e := beginJob(Definition{Name: "amp_generate_setting_secret", Endpoint: "Core/SetConfig"}, x.Connection, cl.conn, x.Scope, x.Scope)
		if e != nil {
			return result(nil, e), nil
		}
		v, e = cl.call(ctx, x.Scope, "Core/SetConfig", map[string]any{"node": x.Node, "value": secret})
		secret = ""
		v, e = finishSubmission(j, v, e)
		if e != nil {
			return result(nil, fmt.Errorf("%w; credential_path=%s (may not be active)", e, name)), nil
		}
		return result(map[string]any{"submission": v, "credential_path": name, "node": x.Node}, nil), nil
	})
}
