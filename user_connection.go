package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type userConnectionInput struct {
	Connection     string `json:"connection"`
	Instance       string `json:"instance_id"`
	Username       string `json:"username"`
	CredentialPath string `json:"credential_path"`
	Name           string `json:"new_connection"`
}

func generatedCredential(x userConnectionInput, source Connection) (string, error) {
	root, e := filepath.EvalSymlinks(filepath.Join(filepath.Dir(configPath()), "credentials"))
	if e != nil {
		return "", errors.New("generated credential directory is unavailable")
	}
	name, e := filepath.EvalSymlinks(x.CredentialPath)
	if e != nil {
		return "", errors.New("generated credential file is unavailable")
	}
	rel, e := filepath.Rel(root, name)
	if e != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("credential_path must be inside this configuration's generated credentials directory")
	}
	info, e := os.Stat(name)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return "", errors.New("invalid generated credential file")
	}
	b, e := os.ReadFile(name)
	if e != nil {
		return "", errors.New("cannot read generated credential")
	}
	var v struct {
		Version  int
		Username string
		Instance string `json:"instance_id"`
		Target   string
		Cipher   string `json:"passwordDpapi"`
		Kind     string
	}
	if json.Unmarshal(b, &v) != nil || v.Version != 1 || v.Kind != "" || v.Username != x.Username || v.Instance != x.Instance || v.Target != targetFingerprint(source) {
		return "", errors.New("generated credential does not match the source target, username and instance")
	}
	return decryptToken(v.Cipher)
}
func (a *App) createUserConnection(ctx context.Context, x userConnectionInput) (any, error) {
	if strings.TrimSpace(x.Name) != x.Name || x.Name == "" {
		return nil, errors.New("new_connection must be a nonempty connection name without surrounding whitespace")
	}
	config, e := loadConfig()
	if e != nil {
		return nil, e
	}
	if _, exists := config.Connections[x.Name]; exists {
		return nil, errors.New("new_connection already exists; configuration unchanged")
	}
	source, e := a.client(x.Connection)
	if e != nil {
		return nil, e
	}
	if source.conn.InstanceID != "" && !strings.EqualFold(source.conn.InstanceID, x.Instance) {
		return nil, errors.New("source connection belongs to another instance")
	}
	password, e := generatedCredential(x, source.conn)
	if e != nil {
		return nil, e
	}
	conn := source.conn
	conn.Username = x.Username
	conn.Token = ""
	conn.InstanceID = x.Instance
	conn.Password, e = encryptCredential(password)
	password = ""
	if e != nil {
		return nil, e
	}
	check, e := newClient(conn)
	if e != nil {
		return nil, e
	}
	info, e := check.call(ctx, "", "Core/GetModuleInfo", map[string]any{})
	check.close()
	if e != nil {
		return nil, fmt.Errorf("native account login verification failed: %w", e)
	}
	im, _ := info.(map[string]any)
	if id, _ := im["InstanceId"].(string); !strings.EqualFold(id, x.Instance) {
		return nil, errors.New("login did not reach the selected application instance")
	}
	j, e := beginJob(Definition{Name: "amp_create_user_connection", Endpoint: "local/configure-instance-user"}, x.Connection, source.conn, x.Instance, x.Instance)
	if e != nil {
		return nil, e
	}
	unlock, e := lockFile(configPath() + ".lock")
	if e != nil {
		_, _ = finishSubmission(j, nil, e)
		return nil, fmt.Errorf("connection store is busy; job_id=%s; no connection saved", j.ID)
	}
	defer unlock()
	config, e = loadConfig()
	if e == nil {
		if _, exists := config.Connections[x.Name]; exists {
			e = errors.New("connection name was created concurrently")
		} else {
			config.Connections[x.Name] = conn
			e = writeJSONAtomic(configPath(), config)
		}
	}
	if e != nil {
		_, _ = finishSubmission(j, nil, e)
		return nil, fmt.Errorf("connection could not be saved; job_id=%s; inspect the connection store before retrying", j.ID)
	}
	// The encrypted password stays in the connection store, never in MCP output.
	j.State = "succeeded"
	j.Note = "native instance password login verified and saved with CurrentUser DPAPI"
	if e = saveJob(j); e != nil {
		return nil, fmt.Errorf("connection saved, but job update failed; job_id=%s; do not create it again", j.ID)
	}
	return map[string]any{"connection": x.Name, "username": x.Username, "instance_id": x.Instance, "job_id": j.ID, "verified": true, "authentication": "password"}, nil
}
func addUserConnectionTool(s registry, a *App) {
	props := map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": guidPattern}, "username": map[string]any{"type": "string", "minLength": 1, "maxLength": 100}, "credential_path": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096, "description": "DPAPI credential_path from amp_user operation=reset_password for this exact account and instance."}, "new_connection": map[string]any{"type": "string", "minLength": 1, "maxLength": 100}}
	addTool(s, &mcp.Tool{Name: "amp_create_user_connection", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id", "username", "credential_path", "new_connection"}, "additionalProperties": false}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x userConnectionInput
		json.Unmarshal(r.Params.Arguments, &x)
		v, e := a.createUserConnection(ctx, x)
		return result(v, e), nil
	})
}
