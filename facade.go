package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The public contract: a few grouped tools, each dispatching an `operation`
// to one internal implementation. Public parameters are snake_case and are
// renamed to AMP's field names here, so the handlers keep AMP's wire names.

const serverInstructions = `AMP MCP manages CubeCoders AMP servers through named connections.
Start with amp_list_connections, then amp_instance_read operation=list to find instance GUIDs.
Grouped tools take an "operation"; the tool description lists what each operation does and which parameters it uses. *_read tools never change AMP.
Targeting: ADS is the AMP controller. Tools that act on an instance take instance_id (InstanceID GUID). Where instance_id is optional, omitting it targets ADS itself. amp_instance start/stop controls the whole AMP instance process; amp_application start/stop controls the game inside it.
Lists return {items,next_offset,more}: pass next_offset as offset while more is true; query filters rows case-insensitively; limit defaults to 50.
Jobs: every AMP mutation is recorded as a local job and returns {job,result}. A submitted or unknown job is not success. Use amp_job_read operation=get with refresh=true to reconcile it; never repeat a mutation whose outcome is uncertain.
Secrets: passwords and secret setting values never pass through MCP; generated credentials are saved locally with DPAPI and shown to the human by "amp-mcp show-user-password PATH".`

type op struct {
	Name, Tool, Desc string
	Rename           map[string]string // internal -> public, for this operation only
	Const            map[string]any    // internal arguments fixed by this operation
	Hide             []string          // internal parameters this operation does not expose
	Require          []string          // extra public parameters this operation requires
	ReadOnly         bool              // read-only although its internal tool also writes
}
type facade struct {
	Name, Desc                        string
	Direct                            string // publish this internal tool unchanged
	Ops                               []op
	Rename                            map[string]string // internal -> public for every operation
	Params                            map[string]string // public parameter descriptions
	ReadOnly, Destructive, Idempotent bool
}

var globalRename = map[string]string{
	"ForceIncludeSelf": "include_self", "ModuleName": "module_name", "datastoreId": "datastore_id", "target_datastore": "datastore_id",
	"TaskId": "task_id", "TaskID": "task_id", "taskid": "task_id", "TriggerID": "trigger_id", "triggerId": "trigger_id", "triggerid": "trigger_id",
	"methodid": "method_id", "Before": "before", "Offset": "offset", "ChunkSize": "chunk_size", "StoreName": "store", "SearchQuery": "query",
	"pageSize": "page_size", "EntryId": "entry_id", "targetads_instance": "target_id", "templateid": "template_id", "new_friendly_name": "friendly_name",
	"display_group": "group", "OverwriteExisting": "overwrite", "Enabled": "enabled", "Id": "trigger_id",
}

var paramDocs = map[string]string{
	"connection":  "Connection name from amp_list_connections.",
	"instance_id": "Instance GUID (InstanceID from amp_instance_read list). Where optional, omit it to target ADS.",
	"offset":      "Pagination start; pass next_offset from the previous page.",
	"limit":       "Maximum rows to return (default 50).",
	"query":       "Case-insensitive text filter over the returned rows.",
}

var facades = []facade{
	{Name: "amp_list_connections", Direct: "amp_list_connections", ReadOnly: true, Idempotent: true,
		Desc: "List configured connection names (never credentials). Start here; every other tool takes one of these names as connection."},
	{Name: "amp_test_connection", Direct: "amp_test_connection", ReadOnly: true, Idempotent: true,
		Desc: "Check connectivity and authentication for a connection. Returns AMPVersion, APIVersion, ModuleName, LoadedPlugins and FeatureSet. Optional instance_id checks that instance instead of ADS."},
	{Name: "amp_open_connection_manager", Direct: "amp_open_connection_manager", Idempotent: true,
		Desc: "Open the local Windows connection manager window so the human can add or edit connections. Returns {window_opened}. Credentials never pass through MCP."},
	{Name: "amp_instance_read", ReadOnly: true, Idempotent: true,
		Desc: "Read AMP instances managed by ADS (an instance is one AMP process hosting one application).",
		Ops: []op{
			{Name: "list", Tool: "amp_list_instances", Desc: "all instances on all targets -> {items,...}; rows have InstanceID, FriendlyName, Module, Running, AppStateName, target_id."},
			{Name: "get", Tool: "amp_get_instance", Desc: "one instance: module, state, IP/ports, endpoints, metrics, disk usage."},
			{Name: "statuses", Tool: "amp_get_instance_status", Desc: "running state and resource metrics of every instance -> {items,...}."},
			{Name: "endpoints", Tool: "amp_get_application_endpoints", Desc: "public address:port endpoints of the instance's application -> {items,...}."},
			{Name: "network", Tool: "amp_get_instance_network", Desc: "port mappings and advertised address -> {items,...}; mapping names feed amp_instance set_network."},
		},
		Params: map[string]string{"include_self": "list: also include the ADS instance itself."}},
	{Name: "amp_provisioning_read", ReadOnly: true, Idempotent: true,
		Desc: "Read ADS provisioning data: installable applications, datastores and deployment templates.",
		Ops: []op{
			{Name: "applications", Tool: "amp_list_applications", Desc: "applications ADS can install -> {items,...}; an item Id is a spec_id for amp_instance create."},
			{Name: "provision_arguments", Tool: "amp_get_provision_arguments", Desc: "provisioning fields accepted for module_name -> {items,...}."},
			{Name: "datastores", Tool: "amp_list_datastores", Desc: "storage locations for instances -> {items,...}."},
			{Name: "datastore", Tool: "amp_get_datastore", Desc: "one datastore by datastore_id.", Rename: map[string]string{"id": "datastore_id"}},
			{Name: "datastore_instances", Tool: "amp_get_datastore_instances", Desc: "instances stored on datastore_id -> {items,...}."},
			{Name: "templates", Tool: "amp_list_deployment_templates", Desc: "deployment templates -> {items,...}; Id is a template_id for amp_template."},
		},
		Params: map[string]string{"module_name": "AMP module name, e.g. Minecraft.", "datastore_id": "Numeric datastore Id."}},
	{Name: "amp_instance", Destructive: true,
		Desc: "Change AMP instances through ADS. start/stop/restart/upgrade act on the whole AMP instance process, not only the game (use amp_application for the game). Each operation returns a tracked job.",
		Ops: []op{
			{Name: "start", Tool: "amp_start_instance", Desc: "start the instance's AMP process."},
			{Name: "stop", Tool: "amp_stop_instance", Desc: "stop the instance's AMP process (its game stops too)."},
			{Name: "restart", Tool: "amp_restart_instance", Desc: "restart the instance's AMP process."},
			{Name: "upgrade", Tool: "amp_upgrade_instance", Desc: "upgrade the instance to the ADS AMP version."},
			{Name: "delete", Tool: "amp_delete_instance", Desc: "permanently delete the instance and its files."},
			{Name: "create", Tool: "amp_create_instance", Desc: "create a new instance of application spec_id on target target_id."},
			{Name: "clone", Tool: "amp_clone_instance", Desc: "copy instance_id to target_id as a new instance; never deletes the source."},
			{Name: "update_info", Tool: "amp_update_instance_info", Desc: "change friendly_name, description, start_on_boot or group."},
			{Name: "set_network", Tool: "amp_set_instance_network", Desc: "change port_mappings and advertised address; AMP will not stop the instance unless allow_stop=true."},
			{Name: "move_datastore", Tool: "amp_move_instance_datastore", Desc: "move the instance's files to datastore_id."},
		},
		Params: map[string]string{
			"spec_id": "Application Id from amp_provisioning_read applications.", "target_id": "Target ADS GUID (target_id from amp_instance_read list).",
			"friendly_name": "Display name (clone: name of the new instance).", "post_create": "What AMP does after creation (default DoNothing).",
			"datastore_id": "Numeric datastore Id from amp_provisioning_read datastores.", "group": "Display group name.", "copy_files": "clone: copy the source's files (default true).",
			"port_mappings": "Object mapping port names (from amp_instance_read network) to port numbers.", "allow_stop": "set_network: allow AMP to stop the instance to apply the change.",
			"start_on_boot": "Start the instance when ADS starts.", "description": "update_info: instance description.",
		}},
	{Name: "amp_template", Destructive: true,
		Desc: "Manage ADS deployment templates. Only templates without secret provisioning inputs are supported. Each operation returns a tracked job.",
		Ops: []op{
			{Name: "create", Tool: "amp_create_deployment_template", Desc: "create an empty template called name."},
			{Name: "clone", Tool: "amp_clone_deployment_template", Desc: "copy template_id under new_name.", Rename: map[string]string{"id": "template_id"}},
			{Name: "apply", Tool: "amp_apply_deployment_template", Desc: "apply template_id to existing instance_id, overwriting its configuration."},
			{Name: "deploy", Tool: "amp_deploy_template", Desc: "create a new instance from template_id."},
		},
		Params: map[string]string{"template_id": "Numeric template Id from amp_provisioning_read templates.", "post_create": "What AMP does after creation (default DoNothing).", "restart_if_previously_running": "apply: restart the instance afterwards if it was running.", "friendly_name": "Name of the resulting instance.", "required_tags": "deploy: only targets with these tags.", "tag": "deploy: tag for the new instance."}},
	{Name: "amp_application_read", ReadOnly: true, Idempotent: true,
		Desc: "Read the application (game server) inside an AMP instance, plus AMP tasks and the audit log.",
		Ops: []op{
			{Name: "status", Tool: "amp_get_application_status", Desc: "application State/StateName, metrics and uptime."},
			{Name: "info", Tool: "amp_get_application_info", Desc: "module name, AMP/API versions and loaded plugins."},
			{Name: "console", Tool: "amp_read_console", Desc: "console output -> {entries,next_cursor,truncated}; newest limit lines (default 100), or mode=since with cursor for only new lines. History is kept per MCP process."},
			{Name: "tasks", Tool: "amp_list_tasks", Desc: "running AMP tasks with state names -> {items,...}; instance_id optional."},
			{Name: "task", Tool: "amp_get_task", Desc: "one task by task_id; instance_id optional."},
			{Name: "audit_log", Tool: "amp_get_audit_log", Desc: "audit entries, newest first, older than before if given -> {items,...}; instance_id optional."},
		},
		Params: map[string]string{"cursor": "console: next_cursor from the previous console read.", "mode": "console: tail = newest lines (rejects cursor); since = lines after cursor.", "task_id": "Task Id from tasks or from a job.", "before": "audit_log: ISO timestamp; only older entries."}},
	{Name: "amp_application", Destructive: true,
		Desc: "Control the application (game server) inside an AMP instance. Each operation returns a tracked job.",
		Ops: []op{
			{Name: "start", Tool: "amp_start_application", Desc: "start the application."},
			{Name: "stop", Tool: "amp_stop_application", Desc: "stop the application cleanly."},
			{Name: "restart", Tool: "amp_restart_application", Desc: "restart the application."},
			{Name: "kill", Tool: "amp_kill_application", Desc: "force-terminate the application without a clean shutdown; unsaved data can be lost."},
			{Name: "update", Tool: "amp_update_application", Desc: "update or reinstall the application files."},
			{Name: "send_console", Tool: "amp_send_console_message", Desc: "send message as a console command."},
			{Name: "cancel_task", Tool: "amp_cancel_task", Desc: "cancel a cancellable AMP task_id; instance_id optional."},
			{Name: "trigger_webhook", Tool: "amp_trigger_webhook", Desc: "fire the instance's webhook plugin event with payload and data."},
		},
		Params: map[string]string{"message": "send_console: console command text."}},
	{Name: "amp_setting_read", ReadOnly: true, Idempotent: true,
		Desc: "Read AMP and application settings. Secret values are always redacted. instance_id is optional (omit for ADS).",
		Ops: []op{
			{Name: "list", Tool: "amp_list_settings", Desc: "setting metadata (node, name, type, limits, sensitive) without values -> {items,...}; filter by section or query."},
			{Name: "get", Tool: "amp_get_settings", Desc: "current values of up to 50 nodes -> {items,...}."},
			{Name: "values", Tool: "amp_list_setting_values", Desc: "allowed values of one choice node; refresh=true reloads deferred version lists."},
		},
		Params: map[string]string{"section": "list: settings section name.", "nodes": "get: setting nodes from list.", "node": "values: setting node from list.", "refresh": "values: reload deferred choices."}},
	{Name: "amp_setting", Destructive: true,
		Desc: "Change AMP and application settings. instance_id is optional (omit for ADS). Does not restart the application.",
		Ops: []op{
			{Name: "set", Tool: "amp_set_setting", Desc: "set one writable non-secret node to value -> tracked job."},
			{Name: "generate_secret", Tool: "amp_generate_setting_secret", Desc: "generate a random value for a secret node, save it encrypted locally, then set it -> {submission,credential_path}; replacing a configured secret needs overwrite=true."},
		},
		Params: map[string]string{"node": "Setting node from amp_setting_read list.", "value": "set: new value as a string.", "overwrite": "generate_secret: replace an already configured secret."}},
	{Name: "amp_schedule_read", ReadOnly: true, Idempotent: true,
		Desc: "Read the AMP scheduler: triggers (when) and their tasks (what). instance_id is optional (omit for ADS).",
		Ops: []op{
			{Name: "list", Tool: "amp_list_schedule", Desc: "triggers with their ids, timing, enabled state and tasks."},
			{Name: "actions", Tool: "amp_list_schedule_actions", Desc: "methods usable as a task method_id, with their parameters."},
		}},
	{Name: "amp_schedule", Destructive: true,
		Desc: "Change the AMP scheduler. instance_id is optional (omit for ADS). Each operation returns a tracked job.",
		Ops: []op{
			{Name: "create_trigger", Tool: "amp_create_interval_trigger", Desc: "add an interval trigger from months, days, hours, minutes, days_of_month and description."},
			{Name: "edit_trigger", Tool: "amp_edit_interval_trigger", Desc: "replace trigger_id's interval and description.", Rename: map[string]string{"id": "trigger_id"}},
			{Name: "set_trigger_enabled", Tool: "amp_set_trigger_enabled", Desc: "enable or disable trigger_id."},
			{Name: "run_trigger", Tool: "amp_run_trigger", Desc: "run trigger_id's tasks now."},
			{Name: "delete_trigger", Tool: "amp_delete_trigger", Desc: "delete trigger_id and its tasks."},
			{Name: "add_task", Tool: "amp_add_schedule_task", Desc: "add method_id with parameter_mapping to trigger_id."},
			{Name: "edit_task", Tool: "amp_edit_schedule_task", Desc: "replace task_id's parameter_mapping."},
			{Name: "delete_task", Tool: "amp_delete_schedule_task", Desc: "remove task_id from trigger_id."},
		},
		Params: map[string]string{
			"trigger_id": "Trigger Id from amp_schedule_read list.", "task_id": "Task Id from amp_schedule_read list.", "method_id": "Method Id from amp_schedule_read actions.",
			"parameter_mapping": "Object mapping the method's parameter names to values.", "months": "Months 1-12 to fire in.", "days": "Weekdays 0-6 to fire on.",
			"hours": "Hours 0-23.", "minutes": "Minutes 0-59.", "days_of_month": "Days 1-31.", "description": "Trigger description.", "enabled": "set_trigger_enabled: true enables, false disables.",
		}},
	{Name: "amp_file_read", ReadOnly: true, Idempotent: true,
		Desc: "Read files inside an AMP instance. Paths are instance-relative, use '/', and cannot contain '..'.",
		Ops: []op{
			{Name: "list", Tool: "amp_list_files", Desc: "entries of directory path (\"\" is the root) -> {items,...}."},
			{Name: "read", Tool: "amp_read_file", Desc: "UTF-8 text from byte offset (default 0), up to chunk_size bytes -> {text,bytes,next_offset,eof}."},
			{Name: "tail", Tool: "amp_tail_file", Desc: "newest lines of a log/text file (default 100 lines within 64 KiB) -> {text,line_count,next_offset,truncated,...}."},
			{Name: "download", Tool: "amp_download_file", Desc: "copy any file, including binary, to a new local file (default limit 16 MiB) -> {path,bytes}."},
		},
		Rename: map[string]string{"Dir": "path", "Filename": "path", "filename": "path"},
		Params: map[string]string{"path": "Instance-relative file or directory path.", "offset": "list: first row (next_offset of the previous page); read: byte offset (next_offset of the previous read).", "chunk_size": "read: bytes to read, 1-65536 (default 65536).", "lines": "tail: lines to return.", "max_bytes": "tail: bytes scanned from the end; download: size limit."}},
	{Name: "amp_file_write", Destructive: true,
		Desc: "Change files inside an AMP instance. Paths are instance-relative, use '/', and cannot contain '..'. Each operation returns a tracked job.",
		Ops: []op{
			{Name: "create_directory", Tool: "amp_create_directory", Desc: "create directory path."},
			{Name: "rename_file", Tool: "amp_rename_file", Desc: "rename file path to new_name in the same directory."},
			{Name: "rename_directory", Tool: "amp_rename_directory", Desc: "rename directory path to new_name."},
			{Name: "copy", Tool: "amp_copy_file", Desc: "copy path into target_directory."},
			{Name: "trash_file", Tool: "amp_trash_file", Desc: "move file path to the AMP trash."},
			{Name: "trash_directory", Tool: "amp_trash_directory", Desc: "move directory path to the AMP trash."},
			{Name: "empty_trash", Tool: "amp_empty_trash", Desc: "permanently delete the contents of trash directory path."},
			{Name: "create_archive", Tool: "amp_create_archive", Desc: "zip path into an archive; overwrite=true replaces an existing archive."},
			{Name: "extract_archive", Tool: "amp_extract_archive", Desc: "extract archive path into destination (default: its directory)."},
			{Name: "write", Tool: "amp_write_file", Desc: "write UTF-8 text (max 1 MiB) to path; overwrite must be stated; not atomic."},
			{Name: "install_from_url", Tool: "amp_install_file_from_url", Desc: "download HTTPS source_url, verify sha512, upload the bytes to path (default limit 64 MiB); overwrite must be stated; not atomic."},
		},
		Rename: map[string]string{"Filename": "path", "filename": "path", "NewPath": "path", "oldDirectory": "path", "Origin": "path", "DirectoryName": "path", "TrashDirectoryName": "path", "PathToArchive": "path", "ArchivePath": "path", "NewFilename": "new_name", "NewDirectoryName": "new_name", "TargetDirectory": "target_directory", "DestinationPath": "destination"},
		Params: map[string]string{"path": "Instance-relative file or directory path.", "new_name": "New name without directory.", "overwrite": "write/install_from_url: required true/false; create_archive: replace an existing archive.", "text": "write: UTF-8 file content.", "source_url": "install_from_url: HTTPS URL without credentials.", "sha512": "install_from_url: publisher SHA-512 (hex).", "max_bytes": "install_from_url: download size limit."}},
	{Name: "amp_store_read", ReadOnly: true, Idempotent: true,
		Desc: "Read the plugin/mod stores of an AMP instance.",
		Ops: []op{
			{Name: "stores", Tool: "amp_list_stores", Desc: "available stores -> {items,...}."},
			{Name: "search", Tool: "amp_search_store", Desc: "search store for query, paged by page and page_size."},
			{Name: "entry", Tool: "amp_get_store_entry", Desc: "details of entry_id in store."},
		},
		Params: map[string]string{"store": "Store name from stores.", "query": "stores: row filter; search: search text.", "entry_id": "Entry Id from search."}},
	{Name: "amp_store", Destructive: true,
		Desc: "Install, update or remove plugin/mod store entries in an AMP instance. Each operation returns a tracked job.",
		Ops: []op{
			{Name: "install", Tool: "amp_install_store_entry", Desc: "install entry_id from store."},
			{Name: "update", Tool: "amp_update_store_entry", Desc: "update installed entry_id."},
			{Name: "uninstall", Tool: "amp_uninstall_store_entry", Desc: "remove installed entry_id and its files."},
		},
		Params: map[string]string{"store": "Store name from amp_store_read stores.", "entry_id": "Entry Id from amp_store_read search."}},
	{Name: "amp_access_read", ReadOnly: true, Idempotent: true,
		Desc: "Read AMP users, roles, sessions and effective permissions. instance_id is optional (omit for ADS).",
		Ops: []op{
			{Name: "users", Tool: "amp_list_users", Desc: "user summaries -> {items,...}."},
			{Name: "user", Tool: "amp_get_user", Desc: "one user: id, disabled, email, roles, last login."},
			{Name: "roles", Tool: "amp_list_roles", Desc: "roles with ids -> {items,...}."},
			{Name: "role_permissions", Tool: "amp_get_role_permissions", Desc: "permission nodes granted to role_id -> {items,...}."},
			{Name: "sessions", Tool: "amp_list_sessions", Desc: "active sessions -> {items,...}; a terminable one has session_handle (valid 15 minutes) for amp_user end_session."},
			{Name: "check_permissions", Tool: "amp_check_permissions", Desc: "{node: bool} for this connection's own account; node \"*\" checks full scope authority."},
		},
		Params: map[string]string{"role_id": "Role GUID from roles.", "username": "AMP username.", "permissions": "check_permissions: permission nodes to test."}},
	{Name: "amp_user", Destructive: true,
		Desc: "Change AMP user accounts and sessions. instance_id is optional (omit for ADS). Mutations return tracked jobs.",
		Ops: []op{
			{Name: "create", Tool: "amp_create_user", Desc: "create username without a password (then use reset_password)."},
			{Name: "update", Tool: "amp_update_user", Desc: "change flags or email_address of username; omitted flags keep their current values."},
			{Name: "delete", Tool: "amp_delete_user", Desc: "delete username (AMP may require disabled=true first)."},
			{Name: "reset_password", Tool: "amp_generate_user_password", Desc: "set a generated password, saved encrypted locally -> {submission,credential_path}; never repeat after an uncertain outcome."},
			{Name: "set_role", Tool: "amp_set_user_role", Desc: "add (is_member=true) or remove user_id from role_id."},
			{Name: "end_session", Tool: "amp_end_session", Desc: "end the session session_handle (logs that user out)."},
			{Name: "save_connection", Tool: "amp_create_user_connection", Desc: "save new_connection logging in as username on instance_id with credential_path from reset_password; verifies login, never overwrites."},
		},
		Params: map[string]string{"username": "AMP username.", "user_id": "User GUID from amp_access_read user.", "role_id": "Role GUID from amp_access_read roles.", "session_handle": "session_handle from amp_access_read sessions.", "credential_path": "save_connection: credential_path returned by reset_password for this account.", "new_connection": "save_connection: name of the new connection."}},
	{Name: "amp_role", Destructive: true,
		Desc: "Change AMP roles. instance_id is optional (omit for ADS). Each operation returns a tracked job.",
		Ops: []op{
			{Name: "create", Tool: "amp_create_role", Desc: "create role name; as_common_role=true makes a global template role."},
			{Name: "rename", Tool: "amp_rename_role", Desc: "rename role_id to new_name."},
			{Name: "delete", Tool: "amp_delete_role", Desc: "delete role_id."},
			{Name: "set_permission", Tool: "amp_set_role_permission", Desc: "set permission_node on role_id: enabled true grants, false denies, null clears."},
		},
		Params: map[string]string{"role_id": "Role GUID from amp_access_read roles.", "permission_node": "Permission node, e.g. Core.AppManagement.StartInstance."}},
	{Name: "amp_job_read", ReadOnly: true, Idempotent: true,
		Desc: "Read the local record of mutations (jobs) for a connection.",
		Ops: []op{
			{Name: "list", Tool: "amp_list_jobs", Desc: "jobs newest first, optionally by state -> {jobs,next_offset,more}; does not poll AMP."},
			{Name: "get", Tool: "amp_get_job", Desc: "one job_id; refresh=true polls AMP to reconcile its state without resubmitting."},
		},
		Params: map[string]string{"job_id": "job_id from a mutation result.", "state": "list: only jobs in this state.", "limit": "list: maximum jobs (default 20).", "refresh": "get: poll AMP for the current state."}},
	{Name: "amp_account_setup_read", ReadOnly: true, Idempotent: true,
		Desc: "Plan dedicated AMP service accounts and roles. Nothing here writes to AMP.",
		Ops: []op{
			{Name: "profiles", Tool: "amp_list_permission_profiles", Desc: "permission profiles: observer reads status/console; operator also runs the application, files, settings and module actions; admin also manages accounts and roles. No connection needed."},
			{Name: "preview", Tool: "amp_preview_account_setup", Desc: "resolve the role and grants for target_connection -> plan_id and exact grants; instance scope takes exactly one application instance."},
			{Name: "get", Tool: "amp_get_account_setup", Desc: "local setup receipt for setup_id: grants, state and job ids. No connection needed."},
			{Name: "expand_preview", Tool: "amp_expand_privileges", Desc: "rights an existing managed role lacks for the module on instance_id -> plan_id.", Const: map[string]any{"mode": "preview"}, Hide: []string{"plan_id"}, ReadOnly: true},
		},
		Params: map[string]string{
			"connection": "Bootstrap administrator connection; for expand_preview an administrator already holding the rights to grant.", "target_connection": "Existing dedicated service-account connection on the same ADS.",
			"role_name": "Name of the role to create.", "profile": "Permission profile from profiles.", "scope": "global: common role for every instance; instance: role for one instance.", "application_instance_ids": "Running example instance per module type (instance scope: exactly one).",
			"bootstrap_role_id": "Temporary role removed from the target at finalize.", "setup_id": "Setup receipt id.", "module": "Exact ModuleName from amp_application_read info.",
			"instance_id": "expand_preview: running instance of the new module.",
		}},
	{Name: "amp_account_setup",
		Desc: "Apply dedicated AMP service-account setups planned with amp_account_setup_read. Non-atomic: errors include receipt and job ids; never replay blindly.",
		Ops: []op{
			{Name: "apply", Tool: "amp_setup_account", Desc: "execute a current preview plan_id: create role, grant rights, assign the target account and verify fresh sessions -> setup_id."},
			{Name: "finalize", Tool: "amp_finalize_account_setup", Desc: "verify sessions on verification_instance_ids, remove the temporary bootstrap role, verify again."},
			{Name: "expand_apply", Tool: "amp_expand_privileges", Desc: "apply an expand_preview plan_id to the role and verify target access.", Const: map[string]any{"mode": "apply"}, Require: []string{"plan_id"}},
		},
		Params: map[string]string{
			"connection": "Bootstrap administrator connection used for the preview.", "plan_id": "plan_id from the matching amp_account_setup_read preview.",
			"target_connection": "Same value as in the preview.", "profile": "Same value as in the preview.", "scope": "Same value as in the preview.", "role_name": "Same value as in the preview.",
			"application_instance_ids": "Same value as in the preview.", "bootstrap_role_id": "Same value as in the preview.", "setup_id": "Setup receipt id from apply (expand_apply: optional, the original setup).",
			"verification_instance_ids": "finalize: instances to verify (global scope: newly created ones).", "module": "expand_apply: exact ModuleName from amp_application_read info.",
			"instance_id": "expand_apply: running instance of the new module.",
		}},
	{Name: "amp_module_action_read", ReadOnly: true, Idempotent: true,
		Desc: "Discover application/plugin API actions an instance offers beyond the fixed tools.",
		Ops: []op{
			{Name: "list", Tool: "amp_list_module_actions", Desc: "actions with parameters and permissions -> {modules,items,next_offset,more}; unsupported ones are flagged."},
			{Name: "describe", Tool: "amp_get_module_action", Desc: "one module/method: argument schema, enum labels, permissions and contract_id for amp_run_module_action."},
		},
		Params: map[string]string{"module": "Module name, e.g. MinecraftModule.", "method": "describe: method name from list.", "refresh": "list: re-read the instance API spec.", "limit": "list: maximum rows (default 20)."}},
	{Name: "amp_run_module_action", Direct: "amp_run_module_action", Destructive: true,
		Params: map[string]string{"module": "Module name from amp_module_action_read list.", "method": "Method name from amp_module_action_read list.", "contract_id": "contract_id from amp_module_action_read describe."},
		Desc:   "Run an application/plugin action described by amp_module_action_read describe, with its contract_id and named arguments (enum labels, not numbers). The contract is re-validated first; mutations return a tracked job and are never replayed."},
}

type boundOp struct {
	op       op
	it       internalTool
	toPublic map[string]string
	required []string
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := []string{}
		for _, s := range x {
			out = append(out, s.(string))
		}
		return out
	}
	return nil
}
func annotations(f facade) *mcp.ToolAnnotations {
	destructive := f.Destructive
	return &mcp.ToolAnnotations{ReadOnlyHint: f.ReadOnly, DestructiveHint: &destructive, IdempotentHint: f.Idempotent}
}
func publishFacade(s *mcp.Server, internal registry, f facade) {
	if f.Direct != "" {
		it, ok := internal[f.Direct]
		if !ok || (f.ReadOnly && !it.readOnly()) {
			panic("facade " + f.Name + ": bad direct tool")
		}
		schema := map[string]any{}
		for k, v := range it.tool.InputSchema.(map[string]any) {
			schema[k] = v
		}
		props := map[string]any{}
		for k, v := range schema["properties"].(map[string]any) {
			p := map[string]any{}
			for pk, pv := range v.(map[string]any) {
				p[pk] = pv
			}
			if doc := paramDoc(f, k, p); doc != "" {
				p["description"] = doc
			}
			props[k] = p
		}
		schema["properties"] = props
		publish(s, &mcp.Tool{Name: f.Name, Description: f.Desc, InputSchema: schema, Annotations: annotations(f)}, func(ctx context.Context, args map[string]any) *mcp.CallToolResult {
			return callInternal(ctx, f.Name, it, args, nil)
		})
		return
	}
	props := map[string]any{}
	defs := map[string]any{}
	used := map[string][]string{}
	ops := map[string]boundOp{}
	names := []string{}
	lines := []string{f.Desc, "Operations:"}
	for _, o := range f.Ops {
		it, ok := internal[o.Tool]
		if !ok || (f.ReadOnly && !it.readOnly() && !o.ReadOnly) {
			panic("facade " + f.Name + ": bad operation " + o.Name)
		}
		schema := it.tool.InputSchema.(map[string]any)
		iprops := schema["properties"].(map[string]any)
		keys := []string{}
		for k := range iprops {
			if _, fixed := o.Const[k]; !fixed && !slices.Contains(o.Hide, k) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		b := boundOp{op: o, it: it, toPublic: map[string]string{}}
		taken := map[string]bool{}
		for _, k := range keys {
			pub := o.Rename[k]
			if pub == "" {
				pub = f.Rename[k]
			}
			if pub == "" {
				pub = globalRename[k]
			}
			if pub == "" {
				pub = k
			}
			if taken[pub] {
				panic(fmt.Sprintf("facade %s operation %s: two parameters map to %s", f.Name, o.Name, pub))
			}
			taken[pub] = true
			b.toPublic[k] = pub
			if _, ok := props[pub]; !ok {
				p := map[string]any{}
				for pk, pv := range iprops[k].(map[string]any) {
					p[pk] = pv
				}
				props[pub] = p
			}
			used[pub] = append(used[pub], o.Name)
		}
		for _, r := range stringsOf(schema["required"]) {
			if pub, ok := b.toPublic[r]; ok {
				b.required = append(b.required, pub)
			}
		}
		b.required = append(b.required, o.Require...)
		if d, ok := schema["$defs"].(map[string]any); ok {
			for k, v := range d {
				defs[k] = v
			}
		}
		ops[o.Name] = b
		names = append(names, o.Name)
		lines = append(lines, "- "+o.Name+": "+o.Desc)
	}
	for pub, p := range props {
		m := p.(map[string]any)
		doc := paramDoc(f, pub, m)
		if len(used[pub]) == 1 {
			doc = strings.TrimPrefix(doc, used[pub][0]+": ")
		}
		if len(used[pub]) < len(f.Ops) {
			doc = strings.TrimSpace(doc + " Used by: " + strings.Join(used[pub], ", ") + ".")
		}
		m["description"] = doc
	}
	props["operation"] = map[string]any{"type": "string", "enum": names, "description": "Operation to run; see the tool description."}
	required := []string{"operation"}
	if len(used["connection"]) == len(f.Ops) {
		required = append(required, "connection")
	}
	schema := map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	if len(defs) > 0 {
		schema["$defs"] = defs
	}
	publish(s, &mcp.Tool{Name: f.Name, Description: strings.Join(lines, "\n"), InputSchema: schema, Annotations: annotations(f)}, func(ctx context.Context, args map[string]any) *mcp.CallToolResult {
		name, _ := args["operation"].(string)
		b := ops[name]
		label := f.Name + " operation=" + name
		fromPublic := map[string]string{}
		accepted := []string{}
		for k, pub := range b.toPublic {
			fromPublic[pub] = k
			accepted = append(accepted, pub)
		}
		sort.Strings(accepted)
		inner := map[string]any{}
		for k, v := range b.op.Const {
			inner[k] = v
		}
		for pub, v := range args {
			if pub == "operation" {
				continue
			}
			k, ok := fromPublic[pub]
			if !ok && pub == "connection" {
				continue // harmless habit: operations without a connection ignore it
			}
			if !ok {
				return result(nil, fmt.Errorf("%s does not use %s; it accepts: %s", label, pub, strings.Join(accepted, ", ")))
			}
			inner[k] = v
		}
		missing := []string{}
		for _, r := range b.required {
			if _, ok := args[r]; !ok {
				missing = append(missing, r)
			}
		}
		if len(missing) > 0 {
			return result(nil, fmt.Errorf("%s requires: %s", label, strings.Join(missing, ", ")))
		}
		return callInternal(ctx, label, b.it, inner, b.toPublic)
	})
}

func paramDoc(f facade, name string, schema map[string]any) string {
	if doc := f.Params[name]; doc != "" {
		return doc
	}
	if doc := paramDocs[name]; doc != "" {
		return doc
	}
	doc, _ := schema["description"].(string)
	return doc
}

// callInternal validates against the implementation's own schema (the real
// bounds) and reports violations with public parameter names.
func callInternal(ctx context.Context, label string, it internalTool, args map[string]any, toPublic map[string]string) *mcp.CallToolResult {
	if e := it.schema.Validate(args); e != nil {
		keys := []string{}
		for k, pub := range toPublic {
			if k != pub {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			return len(keys[i]) > len(keys[j]) || len(keys[i]) == len(keys[j]) && keys[i] < keys[j]
		})
		pairs := []string{}
		for _, k := range keys {
			pairs = append(pairs, "/"+k, "/"+toPublic[k])
		}
		msg := strings.NewReplacer(pairs...).Replace(e.Error())
		return result(nil, fmt.Errorf("invalid arguments for %s: %s", label, msg))
	}
	raw, _ := json.Marshal(args)
	r, e := it.handler(ctx, &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: raw}})
	if e != nil {
		return result(nil, e)
	}
	return r
}

var publicLabels = sync.OnceValue(func() map[string]string {
	m := map[string]string{}
	for _, f := range facades {
		if f.Direct != "" {
			m[f.Direct] = f.Name
		}
		for _, o := range f.Ops {
			m[o.Tool] = f.Name + " operation=" + o.Name
		}
	}
	return m
})

// publicToolName maps an internal job tool to its public tool and operation.
func publicToolName(internal string) string {
	if l, ok := publicLabels()[internal]; ok {
		return l
	}
	return internal
}
