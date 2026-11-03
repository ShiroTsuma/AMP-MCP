package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var apiIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,99}$`)
var guidPattern = `^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`

type moduleAction struct {
	Module        string                    `json:"module"`
	Method        string                    `json:"method"`
	Parameters    []map[string]any          `json:"parameters"`
	Permissions   []any                     `json:"required_permissions"`
	ReturnType    string                    `json:"return_type"`
	Supported     bool                      `json:"supported"`
	Reason        string                    `json:"reason,omitempty"`
	Schema        map[string]any            `json:"-"`
	EnumArguments map[string]map[string]any `json:"-"`
	ReadOnly      bool                      `json:"read_only"`
}

func isActionModule(module string) bool {
	if !apiIdentifier.MatchString(module) {
		return false
	}
	switch module {
	case "Core", "ADSModule", "FileManagerPlugin", "StorePlugin", "EmailSenderPlugin", "WebhookPlugin":
		return false
	}
	return true
}
func moduleReadOnly(endpoint string) bool {
	// Names beginning with Get are not sufficient evidence of a pure read.
	return endpoint == "MinecraftModule/GetFailureReason"
}
func parameterType(t string) (map[string]any, error) {
	if len(t) > 256 || strings.Count(t, "<") > 4 {
		return nil, errors.New("parameter type exceeds supported nesting")
	}
	if strings.HasPrefix(t, "Nullable<") && strings.HasSuffix(t, ">") {
		s, e := parameterType(t[9 : len(t)-1])
		if e != nil {
			return nil, e
		}
		typ, ok := s["type"].(string)
		if !ok {
			return nil, errors.New("unsupported nested nullable type")
		}
		s["type"] = []string{typ, "null"}
		return s, nil
	}
	if strings.HasSuffix(t, "[]") {
		item, e := parameterType(t[:len(t)-2])
		if e != nil {
			return nil, e
		}
		return map[string]any{"type": "array", "items": item, "maxItems": 1000}, nil
	}
	for _, prefix := range []string{"List<", "IEnumerable<", "IList<"} {
		if strings.HasPrefix(t, prefix) && strings.HasSuffix(t, ">") {
			item, e := parameterType(t[len(prefix) : len(t)-1])
			if e != nil {
				return nil, e
			}
			return map[string]any{"type": "array", "items": item, "maxItems": 1000}, nil
		}
	}
	if strings.HasPrefix(t, "Dictionary<String,") && strings.HasSuffix(t, ">") {
		item, e := parameterType(strings.TrimSpace(t[len("Dictionary<String,") : len(t)-1]))
		if e != nil {
			return nil, e
		}
		return map[string]any{"type": "object", "additionalProperties": item, "maxProperties": 100}, nil
	}
	switch t {
	case "String":
		return map[string]any{"type": "string", "maxLength": 65536}, nil
	case "Guid":
		return map[string]any{"type": "string", "pattern": guidPattern}, nil
	case "Boolean":
		return map[string]any{"type": "boolean"}, nil
	case "Byte":
		return map[string]any{"type": "integer", "minimum": 0, "maximum": 255}, nil
	case "Int16":
		return map[string]any{"type": "integer", "minimum": -32768, "maximum": 32767}, nil
	case "UInt16":
		return map[string]any{"type": "integer", "minimum": 0, "maximum": 65535}, nil
	case "Int32":
		return map[string]any{"type": "integer", "minimum": -2147483648, "maximum": 2147483647}, nil
	case "UInt32":
		return map[string]any{"type": "integer", "minimum": 0, "maximum": 4294967295}, nil
	case "Int64":
		return map[string]any{"type": "integer", "minimum": -9007199254740991, "maximum": 9007199254740991}, nil
	case "UInt64":
		return map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991}, nil
	case "Single", "Double", "Decimal":
		return map[string]any{"type": "number"}, nil
	case "DateTime", "DateTimeOffset", "TimeSpan", "Uri", "IPAddress":
		return map[string]any{"type": "string", "maxLength": 4096}, nil
	default:
		return nil, errors.New("parameter has an unsupported record or untyped JSON type")
	}
}

func compileModuleAction(module, method string, meta map[string]any) moduleAction {
	action := moduleAction{Module: module, Method: method, Supported: true, Parameters: []map[string]any{}, Permissions: []any{}, EnumArguments: map[string]map[string]any{}, ReadOnly: moduleReadOnly(module + "/" + method)}
	action.ReturnType, _ = meta["ReturnTypeName"].(string)
	action.Permissions, _ = meta["RequiredPermissions"].([]any)
	props := map[string]any{}
	required := []string{}
	action.Schema = map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	reject := func(reason string) moduleAction { action.Supported = false; action.Reason = reason; return action }
	if !isActionModule(module) || !apiIdentifier.MatchString(method) {
		return reject("not an application/plugin action namespace")
	}
	if secretNode.MatchString(method) || strings.Contains(strings.ToLower(method), "login") || strings.Contains(strings.ToLower(method), "webauthn") {
		return reject("credential or authentication operations must use a dedicated local workflow")
	}
	if secretNode.MatchString(action.ReturnType) {
		return reject("return type may contain credentials")
	}
	parameters, ok := meta["Parameters"].([]any)
	if meta["Parameters"] != nil && !ok {
		return reject("invalid parameter specification")
	}
	if len(parameters) > 40 {
		return reject("action exceeds supported parameter count")
	}
	for _, raw := range parameters {
		p, ok := raw.(map[string]any)
		if !ok {
			return reject("invalid parameter specification")
		}
		name, _ := p["Name"].(string)
		typ, _ := p["TypeName"].(string)
		if !apiIdentifier.MatchString(name) {
			return reject("invalid parameter name")
		}
		if _, exists := props[name]; exists {
			return reject("duplicate parameter name")
		}
		if secretNode.MatchString(name) || strings.EqualFold(name, "SESSIONID") || strings.EqualFold(name, "Authorization") {
			return reject("action accepts credential or transport-authentication input")
		}
		var schema map[string]any
		if choices, ok := p["ParamEnumValues"].(map[string]any); ok && len(choices) > 0 {
			labels := []string{}
			for label, value := range choices {
				switch value.(type) {
				case string, float64, bool:
				default:
					return reject("unsupported enum representation")
				}
				labels = append(labels, label)
			}
			sort.Strings(labels)
			values := []any{}
			for _, label := range labels {
				values = append(values, label)
			}
			schema = map[string]any{"type": "string", "enum": values}
			if strings.HasPrefix(typ, "Nullable<") {
				schema["type"] = []string{"string", "null"}
				schema["enum"] = append(values, nil)
			}
			action.EnumArguments[name] = choices
		} else {
			var e error
			schema, e = parameterType(typ)
			if e != nil {
				return reject(fmt.Sprintf("unsupported parameter %s (%s)", name, typ))
			}
		}
		props[name] = schema
		optional, _ := p["Optional"].(bool)
		if !optional {
			required = append(required, name)
		}
		action.Parameters = append(action.Parameters, map[string]any{"name": name, "amp_type": typ, "optional": optional, "schema": schema})
	}
	action.Schema["required"] = required
	return action
}

func moduleActions(spec any, module string) ([]moduleAction, []string, error) {
	caps, ok := spec.(map[string]any)
	if !ok {
		return nil, nil, errors.New("invalid AMP API specification")
	}
	modules := []string{}
	actions := []moduleAction{}
	for name, raw := range caps {
		if !isActionModule(name) {
			continue
		}
		methods, ok := raw.(map[string]any)
		if !ok || len(methods) == 0 {
			continue
		}
		modules = append(modules, name)
		if module != "" && name != module {
			continue
		}
		for method, raw := range methods {
			if meta, ok := raw.(map[string]any); ok {
				actions = append(actions, compileModuleAction(name, method, meta))
			}
		}
	}
	sort.Strings(modules)
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Module == actions[j].Module {
			return actions[i].Method < actions[j].Method
		}
		return actions[i].Module < actions[j].Module
	})
	if module != "" {
		found := false
		for _, name := range modules {
			if name == module {
				found = true
			}
		}
		if !found {
			return nil, modules, errors.New("module is not available in this instance; discover modules without a module filter")
		}
	}
	return actions, modules, nil
}
func actionSignature(action moduleAction) string {
	b, _ := json.Marshal(struct {
		Schema      map[string]any
		Enums       map[string]map[string]any
		Return      string
		Permissions []any
		ReadOnly    bool
	}{action.Schema, action.EnumArguments, action.ReturnType, action.Permissions, action.ReadOnly})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c *Client) apiSpec(ctx context.Context, scope string, refresh bool) (any, error) {
	if c.conn.InstanceID != "" {
		if scope != "" && !strings.EqualFold(scope, c.conn.InstanceID) {
			return nil, errors.New("this connection is bound to a different application instance")
		}
		scope = ""
	}
	if refresh {
		c.mu.Lock()
		delete(c.capabilities, scope)
		c.mu.Unlock()
	}
	return c.call(ctx, scope, "Core/GetAPISpec", map[string]any{})
}

type boundModuleAction struct {
	Connection, Scope, Target, Signature, Tool string
	Action                                     moduleAction
}

func (a *App) executeModuleAction(ctx context.Context, bound boundModuleAction, args map[string]any) (any, error) {
	cl, e := a.client(bound.Connection)
	if e != nil {
		return nil, e
	}
	if targetFingerprint(cl.conn) != bound.Target {
		return nil, errors.New("connection target changed; describe the module action again with amp_module_action_read operation=describe")
	}
	spec, e := cl.apiSpec(ctx, bound.Scope, true)
	if e != nil {
		return nil, e
	}
	actions, _, e := moduleActions(spec, bound.Action.Module)
	if e != nil {
		return nil, e
	}
	var current moduleAction
	found := false
	for _, action := range actions {
		if action.Method == bound.Action.Method {
			current = action
			found = true
			break
		}
	}
	if !found || !current.Supported || actionSignature(current) != bound.Signature {
		return nil, errors.New("module action contract changed or disappeared; describe it again with amp_module_action_read operation=describe")
	}
	b, _ := json.Marshal(current.Schema)
	var schema jsonschema.Schema
	if e := json.Unmarshal(b, &schema); e != nil {
		return nil, errors.New("invalid action schema")
	}
	resolved, e := schema.Resolve(nil)
	if e != nil {
		return nil, errors.New("invalid action schema")
	}
	if e := resolved.Validate(args); e != nil {
		return nil, errors.New("invalid action arguments; follow names, required fields, types, bounds and enum labels from amp_module_action_read operation=describe")
	}
	body := map[string]any{}
	props := current.Schema["properties"].(map[string]any)
	for name, value := range args {
		if _, ok := props[name]; !ok {
			return nil, errors.New("unknown module action parameter")
		}
		if choices := current.EnumArguments[name]; choices != nil && value != nil {
			label, ok := value.(string)
			if !ok {
				return nil, errors.New("invalid module action enum")
			}
			converted, ok := choices[label]
			if !ok {
				return nil, errors.New("invalid module action enum")
			}
			value = converted
		}
		body[name] = value
	}
	endpoint := current.Module + "/" + current.Method
	if current.ReadOnly {
		return cl.call(ctx, bound.Scope, endpoint, body)
	}
	j, e := beginJob(Definition{Name: bound.Tool, Endpoint: endpoint}, bound.Connection, cl.conn, bound.Scope, bound.Scope)
	if e != nil {
		return nil, e
	}
	v, e := cl.call(ctx, bound.Scope, endpoint, body)
	return finishSubmission(j, v, e)
}

func addModuleTools(s registry, a *App) {
	common := func() map[string]any {
		return map[string]any{"connection": map[string]any{"type": "string", "minLength": 1}, "instance_id": map[string]any{"type": "string", "pattern": guidPattern}, "module": map[string]any{"type": "string", "pattern": apiIdentifier.String()}}
	}
	props := common()
	props["query"] = map[string]any{"type": "string", "maxLength": 256}
	props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
	props["offset"] = map[string]any{"type": "integer", "minimum": 0}
	props["refresh"] = map[string]any{"type": "boolean"}
	addTool(s, &mcp.Tool{Name: "amp_list_module_actions", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "instance_id"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection    string
			Scope         string `json:"instance_id"`
			Module, Query string
			Limit, Offset int
			Refresh       bool
		}
		json.Unmarshal(r.Params.Arguments, &x)
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		spec, e := cl.apiSpec(ctx, x.Scope, x.Refresh)
		if e != nil {
			return result(nil, e), nil
		}
		actions, modules, e := moduleActions(spec, x.Module)
		if e != nil {
			return result(nil, e), nil
		}
		filtered := []moduleAction{}
		for _, action := range actions {
			if x.Query == "" || strings.Contains(strings.ToLower(action.Module+"/"+action.Method), strings.ToLower(x.Query)) {
				filtered = append(filtered, action)
			}
		}
		if x.Limit == 0 {
			x.Limit = 20
		}
		start := min(x.Offset, len(filtered))
		end := min(start+x.Limit, len(filtered))
		return result(map[string]any{"modules": modules, "items": filtered[start:end], "next_offset": end, "more": end < len(filtered)}, nil), nil
	})
	props = common()
	props["method"] = map[string]any{"type": "string", "pattern": apiIdentifier.String()}
	required := []string{"connection", "instance_id", "module", "method"}
	addTool(s, &mcp.Tool{Name: "amp_get_module_action", InputSchema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x moduleActionRequest
		json.Unmarshal(r.Params.Arguments, &x)
		bound, e := a.describeModuleAction(ctx, x)
		if e != nil {
			return result(nil, e), nil
		}
		return result(map[string]any{"action": bound.Action, "arguments_schema": bound.Action.Schema, "contract_id": moduleContractID(bound), "next_step": "Call amp_run_module_action with this target, method, contract_id and named arguments matching arguments_schema."}, nil), nil
	})
	props = common()
	props["method"] = map[string]any{"type": "string", "pattern": apiIdentifier.String()}
	props["contract_id"] = map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	// Names are dynamic; values have an explicit recursive JSON type union. The
	// exact per-action schema is supplied by get and enforced again server-side.
	props["arguments"] = map[string]any{"type": "object", "maxProperties": 40, "additionalProperties": map[string]any{"$ref": "#/$defs/value"}, "description": "Named arguments matching arguments_schema from amp_module_action_read operation=describe; enum labels, not AMP numeric values. Use {} for no parameters."}
	value := map[string]any{"anyOf": []any{
		map[string]any{"type": "string", "maxLength": 65536},
		map[string]any{"type": "number"}, map[string]any{"type": "boolean"}, map[string]any{"type": "null"},
		map[string]any{"type": "array", "maxItems": 1000, "items": map[string]any{"$ref": "#/$defs/value"}},
		map[string]any{"type": "object", "maxProperties": 100, "additionalProperties": map[string]any{"$ref": "#/$defs/value"}},
	}}
	addTool(s, &mcp.Tool{Name: "amp_run_module_action", InputSchema: map[string]any{"type": "object", "properties": props, "required": append(required, "contract_id", "arguments"), "additionalProperties": false, "$defs": map[string]any{"value": value}}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if len(r.Params.Arguments) > 1024*1024 {
			return result(nil, errors.New("action arguments exceed 1 MiB")), nil
		}
		var x moduleActionRequest
		json.Unmarshal(r.Params.Arguments, &x)
		bound, e := a.describeModuleAction(ctx, x)
		if e != nil {
			return result(nil, e), nil
		}
		if moduleContractID(bound) != x.ContractID {
			return result(nil, errors.New("action contract or target changed; call amp_module_action_read operation=describe again")), nil
		}
		v, e := a.executeModuleAction(ctx, bound, x.Arguments)
		return result(v, e), nil
	})
}

type moduleActionRequest struct {
	Connection string         `json:"connection"`
	Scope      string         `json:"instance_id"`
	Module     string         `json:"module"`
	Method     string         `json:"method"`
	ContractID string         `json:"contract_id"`
	Arguments  map[string]any `json:"arguments"`
}

func moduleContractID(b boundModuleAction) string {
	h := sha256.Sum256([]byte(b.Connection + "\x00" + b.Scope + "\x00" + b.Target + "\x00" + b.Action.Module + "/" + b.Action.Method + "\x00" + b.Signature))
	return hex.EncodeToString(h[:])
}
func (a *App) describeModuleAction(ctx context.Context, x moduleActionRequest) (boundModuleAction, error) {
	cl, e := a.client(x.Connection)
	if e != nil {
		return boundModuleAction{}, e
	}
	spec, e := cl.apiSpec(ctx, x.Scope, true)
	if e != nil {
		return boundModuleAction{}, e
	}
	actions, _, e := moduleActions(spec, x.Module)
	if e != nil {
		return boundModuleAction{}, e
	}
	for _, action := range actions {
		if action.Method != x.Method {
			continue
		}
		if !action.Supported {
			return boundModuleAction{}, fmt.Errorf("action unavailable: %s", action.Reason)
		}
		return boundModuleAction{Connection: x.Connection, Scope: x.Scope, Target: targetFingerprint(cl.conn), Signature: actionSignature(action), Tool: "amp_run_module_action", Action: action}, nil
	}
	return boundModuleAction{}, errors.New("method not available; discover actions with amp_module_action_read operation=list")
}
