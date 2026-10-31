package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Job struct {
	ID            string    `json:"job_id"`
	Connection    string    `json:"connection"`
	Target        string    `json:"target_fingerprint"`
	Instance      string    `json:"instance_id,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	Tool          string    `json:"tool"`
	Endpoint      string    `json:"endpoint"`
	State         string    `json:"state"`
	TaskID        string    `json:"task_id,omitempty"`
	Progress      *float64  `json:"progress_percent,omitempty"`
	RemoteState   *int      `json:"remote_state,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
	Note          string    `json:"note,omitempty"`
	SawTransition bool      `json:"saw_transition,omitempty"`
}

func targetFingerprint(c Connection) string {
	identity := c.URL + "\x00" + c.Username
	if c.InstanceID != "" {
		identity += "\x00" + c.InstanceID
	}
	b := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(b[:])
}
func jobsPath() string { return filepath.Join(filepath.Dir(configPath()), "jobs.json") }
func jobTransaction(change func(map[string]Job) error) error {
	path := jobsPath()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	unlock, e := lockFile(path + ".lock")
	if e != nil {
		return errors.New("job store is busy; retry the local status operation")
	}
	defer unlock()
	jobs := map[string]Job{}
	b, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return errors.New("cannot read job store")
	}
	if len(b) > 0 {
		if e = json.Unmarshal(b, &jobs); e != nil {
			return errors.New("invalid job store; preserve it for recovery")
		}
	}
	if e = change(jobs); e != nil {
		return e
	}
	return writeJSONAtomic(path, jobs)
}
func readJobs() (map[string]Job, error) {
	b, e := os.ReadFile(jobsPath())
	if os.IsNotExist(e) {
		return map[string]Job{}, nil
	}
	if e != nil {
		return nil, e
	}
	var jobs map[string]Job
	if e = json.Unmarshal(b, &jobs); e != nil {
		return nil, errors.New("invalid job store")
	}
	return jobs, nil
}
func beginJob(d Definition, connection string, c Connection, scope, instance string) (Job, error) {
	var id [16]byte
	if _, e := rand.Read(id[:]); e != nil {
		return Job{}, e
	}
	now := time.Now().UTC()
	j := Job{ID: hex.EncodeToString(id[:]), Connection: connection, Target: targetFingerprint(c), Instance: instance, Scope: scope, Tool: d.Name, Endpoint: d.Endpoint, State: "submitting", Created: now, Updated: now}
	e := jobTransaction(func(m map[string]Job) error { m[j.ID] = j; return nil })
	return j, e
}
func saveJob(j Job) error {
	expected := j.Updated
	j.Updated = time.Now().UTC()
	return jobTransaction(func(m map[string]Job) error {
		if current, ok := m[j.ID]; ok && current.Updated.After(expected) {
			return errors.New("job changed concurrently; read it again")
		}
		m[j.ID] = j
		return nil
	})
}
func taskID(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if id, ok := m["Id"].(string); ok && m["State"] != nil {
		return id
	}
	if child := m["Result"]; child != nil {
		return taskID(child)
	}
	return ""
}
func observeTask(j *Job, task map[string]any) {
	if n, ok := task["ProgressPercent"].(float64); ok {
		j.Progress = &n
	}
	if n, ok := task["State"].(float64); ok {
		s := int(n)
		j.RemoteState = &s
		switch s {
		case 0:
			j.State = "running"
			j.Note = "task observed running"
		case 3:
			j.State = "failed"
			j.Note = "AMP reports task failure"
		default:
			j.State = "unknown"
			j.Note = "remote task state is not yet mapped; inspect remote_state"
		}
	}
}
func finishSubmission(j Job, v any, e error) (any, error) {
	if e != nil {
		j.State = "unknown"
		j.Note = "submission outcome uncertain; reconcile before retrying"
		var api *APIError
		if errors.As(e, &api) {
			if api.Kind == "session" || api.Kind == "permission" || api.Kind == "rejected" || (api.HTTPStatus >= 400 && api.HTTPStatus < 500) {
				j.State = "failed"
				j.Note = "AMP explicitly rejected the request"
			}
		}
	} else {
		j.State = "submitted"
		j.Note = "AMP accepted the call; completion is not established"
		j.TaskID = taskID(v)
		if j.TaskID != "" {
			if m, ok := v.(map[string]any); ok {
				if child, ok := m["Result"].(map[string]any); ok {
					m = child
				}
				observeTask(&j, m)
			}
		}
		if j.TaskID == "" {
			if m, ok := v.(map[string]any); ok && m["Status"] == true && synchronousEndpoint(j.Endpoint) {
				j.State = "succeeded"
				j.Note = "AMP confirmed this synchronous operation"
			}
		}
	}
	if se := saveJob(j); se != nil {
		return nil, fmt.Errorf("job %s: request may have executed, but outcome could not be saved; do not repeat the mutation", j.ID)
	}
	if e != nil {
		return nil, fmt.Errorf("%s; job_id=%s; state=%s", e, j.ID, j.State)
	}
	if payload, err := json.Marshal(v); err == nil && len(payload) > 96*1024 {
		return map[string]any{"job": jobView(j), "result_omitted": true, "note": "AMP response exceeded the inline limit; inspect the job and use a bounded read, do not resubmit the operation"}, nil
	}
	return map[string]any{"job": jobView(j), "result": v}, nil
}
func synchronousEndpoint(endpoint string) bool {
	switch endpoint {
	case "Core/SetConfig", "Core/CreateRole", "Core/RenameRole", "Core/DeleteRole", "Core/SetAMPRolePermission", "Core/SetAMPUserRoleMembership", "Core/CreateUser", "Core/UpdateUserInfo", "Core/DeleteUser", "Core/AddIntervalTrigger", "Core/EditIntervalTrigger", "Core/AddTask", "Core/EditTask", "Core/DeleteTask", "Core/DeleteTrigger", "Core/SetTriggerEnabled", "FileManagerPlugin/CreateDirectory", "FileManagerPlugin/RenameFile", "FileManagerPlugin/RenameDirectory", "FileManagerPlugin/CopyFile", "FileManagerPlugin/TrashFile", "FileManagerPlugin/TrashDirectory", "FileManagerPlugin/EmptyTrash":
		return true
	}
	return false
}
func (a *App) refreshJob(ctx context.Context, j Job) (Job, error) {
	cl, e := a.client(j.Connection)
	if e != nil {
		return j, e
	}
	if targetFingerprint(cl.conn) != j.Target {
		return j, errors.New("connection target changed; restore the original connection before reconciling this job")
	}
	if j.State == "succeeded" || j.State == "failed" {
		return j, nil
	}
	if j.TaskID != "" {
		v, e := cl.call(ctx, j.Scope, "Core/GetTasks", map[string]any{})
		if e != nil {
			return j, e
		}
		found := false
		if tasks, ok := v.([]any); ok {
			for _, t := range tasks {
				m, ok := t.(map[string]any)
				if ok && m["Id"] == j.TaskID {
					observeTask(&j, m)
					found = true
					break
				}
			}
		}
		if !found {
			j.State = "unknown"
			j.Note = "AMP no longer lists this task; absence is not proof of success"
		}
	} else if j.Endpoint == "ADSModule/StartInstance" || j.Endpoint == "ADSModule/StopInstance" || j.Endpoint == "ADSModule/DeleteInstance" {
		v, e := cl.call(ctx, "", "ADSModule/GetInstances", map[string]any{})
		if e != nil {
			return j, e
		}
		exists := false
		running := false
		for _, item := range flatten(v) {
			m := item.(map[string]any)
			if m["InstanceID"] == j.Instance {
				exists = true
				running, _ = m["Running"].(bool)
				break
			}
		}
		reached := (j.Endpoint == "ADSModule/DeleteInstance" && !exists) || (j.Endpoint == "ADSModule/StartInstance" && exists && running) || (j.Endpoint == "ADSModule/StopInstance" && exists && !running)
		if reached {
			j.State = "succeeded"
			j.Note = "target state observed; this does not prove which client caused it"
		} else {
			j.State = "unknown"
			j.Note = "desired state not observed; no mutation was repeated"
		}
	} else if j.Endpoint == "Core/Start" || j.Endpoint == "Core/Stop" || j.Endpoint == "Core/Kill" || j.Endpoint == "Core/Restart" {
		v, e := cl.call(ctx, j.Scope, "Core/GetStatus", map[string]any{})
		if e != nil {
			return j, e
		}
		m, ok := v.(map[string]any)
		if !ok {
			return j, errors.New("AMP returned an unexpected application status")
		}
		state, ok := m["State"].(float64)
		if !ok {
			return j, errors.New("AMP application status has no numeric State")
		}
		if state == 100 {
			j.State = "failed"
			j.Note = "application is in the AMP Failed state"
		} else if state == 80 {
			j.State = "unknown"
			j.Note = "application is awaiting user input"
		} else {
			reached := (j.Endpoint == "Core/Start" && state == 20) || ((j.Endpoint == "Core/Stop" || j.Endpoint == "Core/Kill") && state == 0)
			if j.Endpoint == "Core/Restart" {
				if state != 20 {
					j.SawTransition = true
				}
				reached = state == 20 && j.SawTransition
			}
			if reached {
				j.State = "succeeded"
				j.Note = "target application state observed"
			} else {
				j.State = "unknown"
				j.Note = "target application state not established; restart requires an observed transition"
			}
		}
	} else if j.State == "submitting" {
		j.State = "unknown"
		j.Note = "process stopped before submission outcome was recorded"
	}
	return j, saveJob(j)
}
func addJobTools(s *mcp.Server, a *App) {
	text := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	addTool(s, &mcp.Tool{Name: "amp_list_jobs", Description: "List locally recorded operations for a connection. Does not poll AMP. Submitted/unknown are not success.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"connection": text(), "state": map[string]any{"type": "string", "enum": []string{"submitting", "submitted", "running", "succeeded", "failed", "unknown"}}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "offset": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"connection"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection, State string
			Limit, Offset     int
		}
		json.Unmarshal(r.Params.Arguments, &x)
		jobs, e := readJobs()
		if e != nil {
			return result(nil, e), nil
		}
		out := []Job{}
		for _, j := range jobs {
			if j.Connection == x.Connection && (x.State == "" || x.State == j.State) {
				out = append(out, j)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Created.Equal(out[j].Created) {
				return out[i].ID < out[j].ID
			}
			return out[i].Created.After(out[j].Created)
		})
		if x.Limit == 0 {
			x.Limit = 20
		}
		start := min(x.Offset, len(out))
		end := min(start+x.Limit, len(out))
		views := []any{}
		for _, j := range out[start:end] {
			views = append(views, jobView(j))
		}
		return result(map[string]any{"jobs": views, "more": end < len(out), "next_offset": end}, nil), nil
	})
	for _, name := range []string{"amp_get_job", "amp_reconcile_job"} {
		refreshByDefault := name == "amp_reconcile_job"
		addTool(s, &mcp.Tool{Name: name, Description: "Get a recorded job. Reconcile polls AMP without re-submitting the original operation; missing tasks remain unknown.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"connection": text(), "job_id": text(), "refresh": map[string]any{"type": "boolean"}}, "required": []string{"connection", "job_id"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var x struct {
				Connection string
				ID         string `json:"job_id"`
				Refresh    *bool
			}
			json.Unmarshal(r.Params.Arguments, &x)
			m, e := readJobs()
			if e != nil {
				return result(nil, e), nil
			}
			j, ok := m[x.ID]
			if !ok || j.Connection != x.Connection {
				return result(nil, errors.New("job not found for this connection")), nil
			}
			refresh := refreshByDefault
			if x.Refresh != nil {
				refresh = *x.Refresh
			}
			if refresh {
				j, e = a.refreshJob(ctx, j)
			}
			return result(jobView(j), e), nil
		})
	}
	props := map[string]any{"connection": text(), "instance_id": map[string]any{"type": "string", "pattern": "^[0-9a-fA-F-]{36}$"}, "task_id": text()}
	addTool(s, &mcp.Tool{Name: "amp_get_task", Description: "Find an AMP task by its returned Id. Omit instance_id for an ADS task.", InputSchema: map[string]any{"type": "object", "properties": props, "required": []string{"connection", "task_id"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var x struct {
			Connection string
			Scope      string `json:"instance_id"`
			ID         string `json:"task_id"`
		}
		json.Unmarshal(r.Params.Arguments, &x)
		cl, e := a.client(x.Connection)
		if e != nil {
			return result(nil, e), nil
		}
		v, e := cl.call(ctx, x.Scope, "Core/GetTasks", map[string]any{})
		if e != nil {
			return result(nil, e), nil
		}
		if tasks, ok := v.([]any); ok {
			for _, t := range tasks {
				if m, ok := t.(map[string]any); ok && m["Id"] == x.ID {
					return result(t, nil), nil
				}
			}
		}
		return result(nil, errors.New("task not listed by AMP; it may have finished or been dismissed")), nil
	})
}
