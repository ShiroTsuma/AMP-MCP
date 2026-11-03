# Using AMP-MCP

Your MCP client discovers tool names and argument schemas automatically. Use those schemas for exact field names and limits. This guide explains workflows and operation state.

## How the tools are organized

Most tools are grouped by area and take an `operation` argument. The tool description lists every operation with what it does and returns; each parameter's description names the operations that use it. Missing or unused parameters are rejected with an error naming them. Tools ending in `_read` never change AMP, so clients can approve them automatically; their write counterparts carry `destructiveHint` when any operation can lose data. All public parameters are snake_case and are mapped to AMP's field names internally.

The server's MCP `instructions` carry the shared rules once: ADS vs instance targeting, `{items,next_offset,more}` pagination and the job model.

## Discover your servers

Call `amp_list_connections`, then `amp_test_connection` and `amp_instance_read` with `operation: "list"`. Most lists accept `query`, `offset` and `limit` (usually 50 by default, 200 maximum). Follow `next_offset` while `more` is true.

Use returned instance GUIDs. Two processes are involved:

- `amp_instance` operation `start` starts the AMP process for an instance.
- `amp_application` operation `start` starts the game inside it.

Reads do not start instances. `Running` describes the AMP process; `AppState` describes the game. A game can be stopped while AMP is running. Instance status `-1` is displayed as `Offline`.

Tasks, settings, schedules and administration tools accept optional `instance_id`: omit it for ADS, include it for an application. Game lifecycle and files require an instance. A native instance connection rejects another target.

## Tools

| Tool | Operations |
| --- | --- |
| `amp_list_connections`, `amp_test_connection`, `amp_open_connection_manager` | single-purpose connection tools |
| `amp_instance_read` | `list`, `get`, `statuses`, `endpoints`, `network` |
| `amp_instance` | `start`, `stop`, `restart`, `upgrade`, `delete`, `create`, `clone`, `update_info`, `set_network`, `move_datastore` |
| `amp_provisioning_read` | `applications`, `provision_arguments`, `datastores`, `datastore`, `datastore_instances`, `templates` |
| `amp_template` | `create`, `clone`, `apply`, `deploy` |
| `amp_application_read` | `status`, `info`, `console`, `tasks`, `task`, `audit_log` |
| `amp_application` | `start`, `stop`, `restart`, `kill`, `update`, `send_console`, `cancel_task`, `trigger_webhook` |
| `amp_setting_read` / `amp_setting` | `list`, `get`, `values` / `set`, `generate_secret` |
| `amp_schedule_read` / `amp_schedule` | `list`, `actions` / `create_trigger`, `edit_trigger`, `set_trigger_enabled`, `run_trigger`, `delete_trigger`, `add_task`, `edit_task`, `delete_task` |
| `amp_file_read` | `list`, `read`, `tail`, `download` |
| `amp_file_write` | `create_directory`, `rename_file`, `rename_directory`, `copy`, `trash_file`, `trash_directory`, `empty_trash`, `create_archive`, `extract_archive`, `write`, `install_from_url` |
| `amp_store_read` / `amp_store` | `stores`, `search`, `entry` / `install`, `update`, `uninstall` |
| `amp_access_read` | `users`, `user`, `roles`, `role_permissions`, `sessions`, `check_permissions` |
| `amp_user` | `create`, `update`, `delete`, `reset_password`, `set_role`, `end_session`, `save_connection` |
| `amp_role` | `create`, `rename`, `delete`, `set_permission` |
| `amp_job_read` | `list`, `get` (`refresh: true` reconciles) |
| `amp_account_setup_read` / `amp_account_setup` | `profiles`, `preview`, `get`, `expand_preview` / `apply`, `finalize`, `expand_apply` |
| `amp_module_action_read`, `amp_run_module_action` | `list`, `describe`; then run |

Provisioning uses specifications advertised by AMP. Post-create actions default to `DoNothing`; installing and starting are separate unless selected explicitly. Templates requiring secret inputs need local AMP configuration.

## Game settings and module actions

For Fabric, call `amp_setting_read` operation `list` with `query: "Fabric"` and the instance GUID. Read selected values with operation `get`. Refresh deferred version/loader choices through operation `values` with `refresh: true`, then change the exact node with `amp_setting` operation `set`.

Metadata excludes current values. Secret values are redacted and cannot be submitted as plaintext settings. `amp_setting` operation `generate_secret` can generate a writable secret and save it with DPAPI before submission. Enter externally supplied account credentials in AMP. Setting changes do not restart games automatically.

Module actions use three steps:

1. `amp_module_action_read` operation `list` discovers supported methods and unsupported-method reasons. Filter by module or query; refresh after an upgrade.
2. Operation `describe` returns `arguments_schema`, enum labels, permissions and `contract_id` for a method, without executing it.
3. `amp_run_module_action` takes the same target, module, method, contract ID and named arguments. It refreshes the contract and rejects stale schemas or mappings before execution.

These are fixed MCP tools; no runtime registration is needed. Typed actions are validated against the authenticated AMP specification. Opaque records, untyped payloads and authentication methods are not exposed.

## Latest console messages and log tails

`amp_application_read` operation `console` defaults to the newest entries: 100 by default, 200 maximum. Follow with `mode: "since"` and the returned `next_cursor`. `tail` omits a cursor; `since` requires one.

The buffer contains messages received by the current MCP process, capped at 2,000. It cannot reconstruct earlier history and is lost on restart. `truncated` indicates omitted history.

For persisted logs, `amp_file_read` operation `tail` reads a bounded suffix near EOF using instance-relative `path` and `lines` (100 default, 500 maximum). It reads at most 64 KiB and reports truncation and file changes; growth or rotation can race the read.

## Files and mods

Paths are relative to the instance and passed as `path`. `amp_file_read` operation `read` returns UTF-8 text with byte offsets. Operation `download` creates a local artifact for binary or larger files (16 MiB default limit, 256 MiB maximum).

`amp_file_write` operation `write` accepts up to 1 MiB of UTF-8 and an explicit `overwrite` choice. Operation `install_from_url` downloads HTTPS, verifies the required publisher SHA-512, then uploads to an exact path (64 MiB default, 256 MiB maximum). Get compatible versions and hashes from the publishers.

Chunked uploads are not atomic and are not retried automatically. Inspect partial files before another upload or game start.

## Track completion

Mutations return a job stored beside the connection file. `amp_job_read` operation `get` with `refresh: true` reconciles it without resubmitting the original operation.

| Job state | Meaning |
| --- | --- |
| `submitted` | AMP accepted the request; completion is not established |
| `running` | Observed in progress |
| `succeeded` | The tracked completion condition was observed |
| `failed` | AMP explicitly reported failure |
| `unknown` | Available evidence cannot establish completion |

A disappearing task is not proof of success. Reconciliation observes tasks or lifecycle state; a matching state does not prove which client caused it. A missed restart transition can leave completion uncertain.

Application states, AMP task states and MCP job states are distinct. Application `20` means `Ready`. The task names below follow AMP's `TaskState` enum; future unrecognized values retain their raw number and are shown as `Unknown`.

| AMP task code | Name | MCP job state |
| --- | --- | --- |
| `0` | `Running` | `running` |
| `1` | `Waiting` | `submitted` |
| `2` | `Queued` | `submitted` |
| `3` | `Failed` | `failed` |
| `4` | `Finished` | `succeeded` |
| `5` | `PendingCancellation` | `running` |
| `6` | `Cancelled` | `failed`, with cancellation noted |
| `7` | `Acknowledged` | `unknown`; acknowledgement alone does not retain a completion result |
| `100` | `UserActionRequired` | `unknown`; resolve the prompt in AMP |
| `254` | `TimedOut` | `failed`; inspect the target before retrying |

Reconciliation also recognizes terminal task codes saved by older releases, even after AMP dismisses those tasks. A task disappearing without a recorded terminal result, or reaching 100% progress alone, still does not establish success.

## Permissions and sessions

`amp_access_read` operation `check_permissions` checks effective rights in the selected context. ADS success does not establish application authority. Its special `*` node checks full-scope authority, using namespace checks for ADS. Operator covers game lifecycle, commands, files and the selected module's settings/actions; admin additionally manages AMP accounts and roles. Existing managed operators can run `amp_account_setup_read` operation `expand_preview` and `amp_account_setup` operation `expand_apply` to obtain newly defined module rights. See [account setup](setup.md).

Run operator expansion using an authorized administrator connection and the operator's original setup receipt. An operator cannot edit its own role. Global/local scope remains unchanged. See the [role comparison](setup.md#role-capabilities) for the complete distinction.

Recognized session expiry can refresh a safe read once. Mutations and incremental console reads are not replayed. A timeout can mean the request executed; inspect state before another write.

Sessions are process-local and logged out on normal shutdown. Listings hide authentication IDs. If AMP supplies a terminable ID, the tool returns a temporary opaque handle; otherwise `can_terminate` is false.
