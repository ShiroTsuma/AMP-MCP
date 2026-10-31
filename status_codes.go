package main

// Application states follow AMP 2.8's shipped table; -1 is presented as Offline
// for unavailable instances. Task states are a separate enum.
var applicationStates = map[int]string{-1: "Offline", 0: "Stopped", 5: "PreStart", 7: "Configuring", 10: "Starting", 20: "Ready", 30: "Restarting", 40: "Stopping", 45: "PreparingForSleep", 50: "Sleeping", 60: "Waiting", 70: "Installing", 75: "Updating", 80: "AwaitingUserInput", 100: "Failed", 200: "Suspended", 250: "Maintenance", 999: "Indeterminate"}

func applicationStateName(n int) string {
	if s, ok := applicationStates[n]; ok {
		return s
	}
	return "Unknown"
}

// Names verified from the shipped AMP 2.8 ModuleShared.dll TaskState enum.
func taskStateName(n int) string {
	switch n {
	case 0:
		return "Running"
	case 1:
		return "Waiting"
	case 2:
		return "Queued"
	case 3:
		return "Failed"
	case 4:
		return "Finished"
	case 5:
		return "PendingCancellation"
	case 6:
		return "Cancelled"
	case 7:
		return "Acknowledged"
	case 100:
		return "UserActionRequired"
	case 254:
		return "TimedOut"
	}
	return "Unknown"
}
func annotateTaskState(v any) {
	if a, ok := v.([]any); ok {
		for _, item := range a {
			annotateTaskState(item)
		}
		return
	}
	if m, ok := v.(map[string]any); ok {
		if n, ok := m["State"].(float64); ok {
			m["StateName"] = taskStateName(int(n))
		}
	}
}
func jobView(j Job) map[string]any {
	v := map[string]any{"job_id": j.ID, "connection": j.Connection, "tool": j.Tool, "endpoint": j.Endpoint, "state": j.State, "created": j.Created, "updated": j.Updated, "api_scope": "ads"}
	if j.Instance != "" {
		v["instance_id"] = j.Instance
	}
	if j.Scope != "" {
		v["api_scope"] = "instance"
		if j.Scope != j.Instance {
			v["scope"] = j.Scope
		}
	}
	if j.TaskID != "" {
		v["task_id"] = j.TaskID
	}
	if j.Progress != nil {
		v["progress_percent"] = *j.Progress
	}
	if j.RemoteState != nil {
		v["remote_state"] = *j.RemoteState
		v["remote_state_name"] = taskStateName(*j.RemoteState)
	}
	if j.Note != "" {
		v["note"] = j.Note
	}
	return v
}
