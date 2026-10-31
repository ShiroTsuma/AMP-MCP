# Using AMP-MCP

Your MCP client discovers tool names and argument schemas automatically. Use those schemas for exact field names and limits. This guide explains workflows and operation state.

## Discover your servers

Call `amp_list_connections`, then `amp_test_connection` and `amp_list_instances` with a connection name. Most lists accept `query`, `offset` and `limit` (usually 50 by default, 200 maximum). Follow `next_offset` while `more` is true.

Use returned instance GUIDs. Two processes are involved:

- `amp_start_instance` starts the AMP process for an instance.
- `amp_start_application` starts the game inside it.

Reads do not start instances. `Running` describes the AMP process; `AppState` describes the game. A game can be stopped while AMP is running. Instance status `-1` is displayed as `Offline`.

Tasks, settings, schedules and administration tools accept optional `instance_id`: omit it for ADS, include it for an application. Game lifecycle and files require an instance. A native instance connection rejects another target.

## Tool families

| Area | Starting points |
| --- | --- |
| Connections | `amp_list_connections`, `amp_open_connection_manager`, `amp_test_connection` |
| Provisioning | `amp_list_applications`, `amp_get_provision_arguments`, `amp_create_instance` |
| Status and lifecycle | `amp_get_application_status`, `amp_start_application`, `amp_stop_application`, `amp_update_application` |
| Settings | `amp_list_settings`, `amp_get_settings`, `amp_list_setting_values`, `amp_set_setting` |
| Console and logs | `amp_read_console`, `amp_send_console_message`, `amp_tail_file` |
| Files | `amp_list_files`, `amp_read_file`, `amp_write_file`, `amp_download_file`, `amp_install_file_from_url` |
| Module/plugin actions | `amp_list_module_actions`, `amp_get_module_action`, `amp_run_module_action` |
| Access (requires administrative rights for changes) | `amp_list_users`, `amp_list_roles`, `amp_check_permissions`, account setup tools |
| Automation | `amp_list_schedule`, `amp_list_schedule_actions`, `amp_list_deployment_templates` |
| Tasks and jobs | `amp_list_tasks`, `amp_get_task`, `amp_list_jobs`, `amp_get_job`, `amp_reconcile_job` |

Provisioning uses specifications advertised by AMP. Post-create actions default to `DoNothing`; installing and starting are separate unless selected explicitly. Templates requiring secret inputs need local AMP configuration.

## Game settings and module actions

For Fabric, search `amp_list_settings` with `query: "Fabric"` and the instance GUID. Read selected values with `amp_get_settings`. Refresh deferred version/loader choices through `amp_list_setting_values` with `refresh: true`, then change the exact node with `amp_set_setting`.

Metadata excludes current values. Secret values are redacted and cannot be submitted as plaintext settings. `amp_generate_setting_secret` can generate a writable secret and save it with DPAPI before submission. Enter externally supplied account credentials in AMP. Setting changes do not restart games automatically.

Module actions use three steps:

1. `amp_list_module_actions` discovers supported methods and unsupported-method reasons. Filter by module or query; refresh after an upgrade.
2. `amp_get_module_action` returns `arguments_schema`, enum labels, permissions and `contract_id` for a method, without executing it.
3. `amp_run_module_action` takes the same target, module, method, contract ID and named arguments. It refreshes the contract and rejects stale schemas or mappings before execution.

These are fixed MCP tools; no runtime registration is needed. Typed actions are validated against the authenticated AMP specification. Opaque records, untyped payloads and authentication methods are not exposed.

## Latest console messages and log tails

`amp_read_console` defaults to the newest entries: 100 by default, 200 maximum. Follow with `mode: "since"` and the returned `next_cursor`. `tail` omits a cursor; `since` requires one.

The buffer contains messages received by the current MCP process, capped at 2,000. It cannot reconstruct earlier history and is lost on restart. `truncated` indicates omitted history.

For persisted logs, `amp_tail_file` reads a bounded suffix near EOF using instance-relative `filename` and `lines` (100 default, 500 maximum). It reads at most 64 KiB and reports truncation and file changes; growth or rotation can race the read.

## Files and mods

Paths are relative to the instance. `amp_read_file` returns UTF-8 text with byte offsets. `amp_download_file` creates a local artifact for binary or larger files (16 MiB default limit, 256 MiB maximum).

`amp_write_file` accepts up to 1 MiB of UTF-8 and an explicit `overwrite` choice. `amp_install_file_from_url` downloads HTTPS, verifies the required publisher SHA-512, then uploads to an exact path (64 MiB default, 256 MiB maximum). Get compatible versions and hashes from the publishers.

Chunked uploads are not atomic and are not retried automatically. Inspect partial files before another upload or game start.

## Track completion

Mutations return a job stored beside the connection file. `amp_get_job` with `refresh: true` or `amp_reconcile_job` observes it without resubmitting the original operation.

| Job state | Meaning |
| --- | --- |
| `submitted` | AMP accepted the request; completion is not established |
| `running` | Observed in progress |
| `succeeded` | The tracked completion condition was observed |
| `failed` | AMP explicitly reported failure |
| `unknown` | Available evidence cannot establish completion |

A disappearing task is not proof of success. Reconciliation observes tasks or lifecycle state; a matching state does not prove which client caused it. A missed restart transition can leave completion uncertain.

Application states, AMP task states and MCP job states are distinct. Application `20` means `Ready`; AMP task `0` means `Running` and task `3` means `Failed`. Unverified task values remain `Unknown` alongside their raw number.

## Permissions and sessions

`amp_check_permissions` checks effective rights in the selected context. ADS success does not establish application authority. Its special `*` node checks full-scope authority, using namespace checks for ADS. Operator covers game lifecycle, commands, files and the selected module's settings/actions; admin additionally manages AMP accounts and roles. Existing managed operators can preview/apply `amp_expand_privileges` to obtain newly defined module rights. See [account setup](setup.md).

Run operator expansion using an authorized administrator connection and the operator's original setup receipt. An operator cannot edit its own role. Global/local scope remains unchanged. See the [role comparison](setup.md#role-capabilities) for the complete distinction.

Recognized session expiry can refresh a safe read once. Mutations and incremental console reads are not replayed. A timeout can mean the request executed; inspect state before another write.

Sessions are process-local and logged out on normal shutdown. Listings hide authentication IDs. If AMP supplies a terminable ID, the tool returns a temporary opaque handle; otherwise `can_terminate` is false.
