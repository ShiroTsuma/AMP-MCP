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

// Passwords never enter tool arguments, jobs, results or command-line strings.
func addUserPasswordTool(s registry, a *App) {
	props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": guidPattern}, "username": map[string]any{"type": "string", "minLength": 1, "maxLength": 100}}
	addTool(s, &mcp.Tool{Name: "amp_generate_user_password", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "username"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Scope      string `json:"instance_id"`
			Username   string
		}
		json.Unmarshal(r.Params.Arguments, &x)
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		spec, e := cl.apiSpec(ctx, x.Scope, false)
		if e != nil {
			return result(nil, e), nil
		}
		if !supportsEndpoint(spec, "Core/ResetUserPassword") {
			return result(nil, errors.New("password reset is unavailable on this target")), nil
		}
		random := make([]byte, 24)
		if _, e = rand.Read(random); e != nil {
			return result(nil, e), nil
		}
		defer clear(random)
		password := "Aa1!" + base64.RawURLEncoding.EncodeToString(random)
		cipher, e := encryptCredential(password)
		if e != nil {
			return result(nil, e), nil
		}
		dir := filepath.Join(filepath.Dir(configPath()), "credentials")
		if e = os.MkdirAll(dir, 0700); e != nil {
			return result(nil, errors.New("cannot create credential directory")), nil
		}
		f, e := os.CreateTemp(dir, "user-*.json")
		if e != nil {
			return result(nil, errors.New("cannot create encrypted credential file")), nil
		}
		filename := f.Name()
		f.Close()
		saved := map[string]any{"version": 1, "connection": x.Connection, "instance_id": x.Scope, "username": x.Username, "target": targetFingerprint(cl.conn), "passwordDpapi": cipher}
		if e = writeJSONAtomic(filename, saved); e != nil {
			os.Remove(filename)
			return result(nil, errors.New("cannot save encrypted credential; no reset submitted")), nil
		}
		j, e := beginJob(Definition{Name: "amp_generate_user_password", Endpoint: "Core/ResetUserPassword"}, x.Connection, cl.conn, x.Scope, x.Username)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := cl.call(ctx, x.Scope, "Core/ResetUserPassword", map[string]any{"Username": x.Username, "NewPassword": password})
		v, e = finishSubmission(j, v, e)
		if e != nil {
			return result(nil, fmt.Errorf("%w; encrypted credential_path=%s (may not be active)", e, filename)), nil
		}
		return result(map[string]any{"submission": v, "credential_path": filename, "username": x.Username}, nil), nil
	})
}
