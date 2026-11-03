package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
)

//go:embed catalog.json
var catalog []byte

// Set by release builds; the CLI and MCP handshake report the same version.
var version = "dev"

type Definition struct {
	Name              string
	Endpoint          string
	Instance          bool
	Resolve           string
	ReadOnly          bool
	ListResult        bool
	Schema            map[string]any
	Arguments         map[string]string
	EnumArguments     map[string]map[string]any
	ConstantArguments map[string]any
}
type App struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func (a *App) client(name string) (*Client, error) {
	c, e := loadConfig()
	if e != nil {
		return nil, e
	}
	conn, ok := c.Connections[name]
	if !ok {
		return nil, fmt.Errorf("unknown connection; call amp_list_connections")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if old := a.clients[name]; old != nil {
		if old.conn == conn {
			return old, nil
		}
		old.close()
	}
	cl, e := newClient(conn)
	if e == nil {
		a.clients[name] = cl
	}
	return cl, e
}
func result(v any, e error) *mcp.CallToolResult {
	if e != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: e.Error()}}}
	}
	v = redact(v)
	b, _ := json.Marshal(v)
	if len(b) > 128*1024 {
		return result(nil, fmt.Errorf("result exceeds 128 KiB; narrow the request"))
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}
func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if x["Node"] != nil && sensitiveSetting(x) {
			delete(x, "CurrentValue")
			delete(x, "DefaultValue")
			delete(x, "EnumValues")
			x["redacted"] = true
		}
		for k, val := range x {
			l := strings.ToLower(k)
			if strings.Contains(l, "password") || strings.Contains(l, "token") || strings.Contains(l, "secret") || strings.Contains(l, "sessionid") {
				delete(x, k)
			} else {
				x[k] = redact(val)
			}
		}
	case []any:
		for i := range x {
			x[i] = redact(x[i])
		}
	}
	return v
}
func flatten(v any) []any {
	out := []any{}
	arr, ok := v.([]any)
	if !ok {
		return out
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if children, ok := m["AvailableInstances"].([]any); ok {
			for _, child := range children {
				if c, ok := child.(map[string]any); ok {
					c["target_name"] = m["FriendlyName"]
					c["target_id"] = m["Id"]
					out = append(out, c)
				}
			}
		} else {
			out = append(out, m)
		}
	}
	return out
}
func (a *App) execute(ctx context.Context, d Definition, args map[string]any) (any, error) {
	name, _ := args["connection"].(string)
	cl, e := a.client(name)
	if e != nil {
		return nil, e
	}
	scope := ""
	if d.Instance {
		scope, _ = args["instance_id"].(string)
	}
	body := map[string]any{}
	for k, v := range d.ConstantArguments {
		body[k] = v
	}
	for k, n := range d.Arguments {
		if v, ok := args[k]; ok {
			if choices := d.EnumArguments[k]; choices != nil {
				label, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("invalid enum field %s", k)
				}
				converted, ok := choices[label]
				if !ok {
					return nil, fmt.Errorf("invalid enum field %s", k)
				}
				v = converted
			}
			body[n] = v
		}
	}
	if d.Resolve != "" {
		v, e := cl.call(ctx, "", "ADSModule/GetInstances", map[string]any{})
		if e != nil {
			return nil, e
		}
		found := false
		for _, item := range flatten(v) {
			m := item.(map[string]any)
			if m["InstanceID"] == args["instance_id"] || m["InstanceId"] == args["instance_id"] {
				body[d.Resolve] = m["InstanceName"]
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("instance GUID not found; call amp_instance_read operation=list")
		}
	}
	if d.Endpoint == "Core/SetConfig" || d.Endpoint == "Core/GetSettingValues" {
		node, _ := body["node"].(string)
		if node == "" {
			node, _ = body["SettingNode"].(string)
		}
		if err := checkSetting(ctx, cl, scope, node, d.Endpoint == "Core/SetConfig"); err != nil {
			return nil, err
		}
	}
	if d.Name == "amp_read_file" {
		if e = fileReadBounds(body); e != nil {
			return nil, e
		}
	}
	if d.Endpoint == "Core/GetAuditLogEntries" {
		// AMP pages backwards from Before; fetch enough rows for the requested local page.
		body["Count"] = numeric(args["offset"], 0) + numeric(args["limit"], 50)
		if _, ok := body["Before"]; !ok {
			body["Before"] = nil
		}
	}
	if d.Endpoint == "Core/UpdateUserInfo" {
		if e = fillUserFlags(ctx, cl, scope, body); e != nil {
			return nil, e
		}
	}
	if d.Endpoint == "Core/CancelTask" {
		v, err := cl.call(ctx, scope, "Core/GetTasks", map[string]any{})
		if err != nil {
			return nil, err
		}
		cancellable := false
		taskID := body["TaskId"]
		if tasks, ok := v.([]any); ok {
			for _, t := range tasks {
				if task, ok := t.(map[string]any); ok && task["Id"] == taskID {
					cancellable = task["IsCancellable"] == true
					break
				}
			}
		}
		if !cancellable {
			return nil, fmt.Errorf("task is not currently listed as cancellable")
		}
	}
	var job Job
	if !d.ReadOnly {
		caps, err := cl.call(ctx, scope, "Core/GetAPISpec", map[string]any{})
		if err != nil {
			return nil, err
		}
		if !supportsEndpoint(caps, d.Endpoint) {
			return nil, fmt.Errorf("this endpoint is not supported by the selected instance or plugin")
		}
		instance, _ := args["instance_id"].(string)
		job, e = beginJob(d, name, cl.conn, scope, instance)
		if e != nil {
			return nil, e
		}
	}
	v, e := cl.call(ctx, scope, d.Endpoint, body)
	if !d.ReadOnly {
		return finishSubmission(job, v, e)
	}
	if e != nil {
		return nil, e
	}
	if d.Endpoint == "Core/GetSettingsSpec" {
		return safeSettingsMetadata(v, args)
	}
	if d.Endpoint == "Core/GetStatus" {
		if m, ok := v.(map[string]any); ok {
			if n, ok := m["State"].(float64); ok {
				m["StateName"] = applicationStateName(int(n))
			}
		}
	}
	if d.Endpoint == "Core/GetTasks" {
		annotateTaskState(v)
	}
	if d.Name == "amp_read_file" {
		return readTextFileResult(v, numeric(body["Offset"], 0), numeric(body["ChunkSize"], transferChunk))
	}
	if d.Endpoint == "Core/GetAMPUserInfo" {
		if m, ok := v.(map[string]any); ok {
			v = selectKeys(m, "Username", "UserID", "Id", "Disabled", "EmailAddress", "Roles", "RoleMembership", "LastLogin")
		}
	}
	if d.Endpoint == "ADSModule/GetInstance" {
		if m, ok := v.(map[string]any); ok {
			v = selectKeys(m, "InstanceID", "InstanceName", "FriendlyName", "Module", "ModuleDisplayName", "Running", "AppState", "Suspended", "Description", "TargetID", "Group", "Tags", "IP", "Port", "ApplicationIP", "ApplicationEndpoints", "Metrics", "DiskUsageMB", "AMPVersion")
		}
	}
	if d.Endpoint == "ADSModule/GetInstances" {
		items := flatten(v)
		for _, item := range items {
			m := item.(map[string]any)
			for k := range m {
				switch k {
				case "InstanceID", "InstanceName", "FriendlyName", "Module", "ModuleDisplayName", "Running", "AppState", "Suspended", "target_name", "target_id", "TargetID", "Group", "Tags":
				default:
					delete(m, k)
				}
			}
			if n, ok := m["AppState"].(float64); ok {
				m["AppStateName"] = applicationStateName(int(n))
			}
		}
		v = items
	}
	if d.ListResult {
		return pageItems(v, args)
	}
	return v, nil
}

// UpdateUserInfo replaces every flag; keep the current value of omitted ones.
func fillUserFlags(ctx context.Context, cl *Client, scope string, body map[string]any) error {
	fields := []string{"Disabled", "PasswordExpires", "CannotChangePassword", "MustChangePassword", "EmailAddress"}
	complete := true
	for _, f := range fields {
		if _, ok := body[f]; !ok {
			complete = false
		}
	}
	if complete {
		return nil
	}
	v, e := cl.call(ctx, scope, "Core/GetAMPUserInfo", map[string]any{"Username": body["Username"]})
	if e != nil {
		return e
	}
	current, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("cannot read the user's current flags; supply disabled, password_expires, cannot_change_password and must_change_password explicitly")
	}
	for _, f := range fields {
		if _, ok := body[f]; ok {
			continue
		}
		value, ok := current[f]
		if !ok && f != "EmailAddress" {
			return fmt.Errorf("AMP did not report the user's current %s; supply it explicitly", f)
		}
		if ok {
			body[f] = value
		}
	}
	return nil
}
func pageItems(v any, args map[string]any) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("AMP returned an unexpected list shape")
	}
	query, _ := args["query"].(string)
	filtered := []any{}
	for _, row := range arr {
		if query != "" {
			text, _ := json.Marshal(redact(row))
			if !strings.Contains(strings.ToLower(string(text)), strings.ToLower(query)) {
				continue
			}
		}
		filtered = append(filtered, row)
	}
	offset := 0
	limit := 50
	if n, ok := args["offset"].(float64); ok {
		offset = int(n)
	}
	if n, ok := args["limit"].(float64); ok {
		limit = int(n)
	}
	start := min(offset, len(filtered))
	end := min(start+limit, len(filtered))
	return map[string]any{"items": filtered[start:end], "next_offset": end, "more": end < len(filtered)}, nil
}
func internalTools(a *App) registry {
	s := registry{}
	var defs []Definition
	if e := json.Unmarshal(catalog, &defs); e != nil {
		panic(e)
	}
	for _, d := range defs {
		addTool(s, &mcp.Tool{Name: d.Name, InputSchema: d.Schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: d.ReadOnly}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			if e := json.Unmarshal(r.Params.Arguments, &args); e != nil {
				return result(nil, e), nil
			}
			v, e := a.execute(ctx, d, args)
			return result(v, e), nil
		})
	}
	empty := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	addTool(s, &mcp.Tool{Name: "amp_list_connections", InputSchema: empty, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		c, e := loadConfig()
		names := []string{}
		for n := range c.Connections {
			names = append(names, n)
		}
		sort.Strings(names)
		return result(names, e), nil
	})
	addTool(s, &mcp.Tool{Name: "amp_open_connection_manager", InputSchema: empty}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		e := openManager()
		return result(map[string]any{"window_opened": e == nil}, e), nil
	})
	addExtraTools(s, a)
	addJobTools(s, a)
	addFileTools(s, a)
	addTailFileTool(s, a)
	addInstallFileTool(s, a)
	addUserPasswordTool(s, a)
	addSettingSecretTool(s, a)
	addUserConnectionTool(s, a)
	addPermissionTool(s, a)
	addAccountSetupTools(s, a)
	addExpandPrivilegesTool(s, a)
	addSessionTools(s, a)
	addModuleTools(s, a)
	return s
}
func server(a *App) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "amp-mcp", Version: version}, &mcp.ServerOptions{Instructions: serverInstructions})
	internal := internalTools(a)
	for _, f := range facades {
		publishFacade(s, internal, f)
	}
	return s
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
func run() error {
	log.SetOutput(os.Stderr)
	a := &App{clients: map[string]*Client{}}
	defer func() {
		for _, c := range a.clients {
			c.close()
		}
	}()
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		if e := server(a).Run(ctx, &mcp.StdioTransport{}); e != nil && ctx.Err() == nil {
			return e
		}
	case "configure":
		if e := openManager(); e != nil {
			return e
		}
	case "version":
		fmt.Println("amp-mcp " + version)
	case "show-user-password":
		if len(os.Args) != 3 {
			return fmt.Errorf("usage: amp-mcp show-user-password PATH")
		}
		return showUserPassword(os.Args[2])
	case "test-connection":
		if len(os.Args) != 3 {
			return fmt.Errorf("usage: amp-mcp test-connection NAME")
		}
		c, e := a.client(os.Args[2])
		if e != nil {
			return e
		}
		v, e := c.call(context.Background(), "", "Core/GetModuleInfo", map[string]any{})
		if m, ok := v.(map[string]any); ok {
			v = selectKeys(m, "AMPVersion", "APIVersion", "ModuleName", "LoadedPlugins")
		}
		r := result(v, e)
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
		if e != nil {
			return fmt.Errorf("connection test failed")
		}
	default:
		return fmt.Errorf("usage: amp-mcp serve|configure|test-connection NAME|show-user-password PATH|version")
	}
	return nil
}
