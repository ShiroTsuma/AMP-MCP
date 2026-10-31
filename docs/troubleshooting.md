# Troubleshooting

## The client cannot start AMP-MCP

Use an absolute path to the extracted `amp-mcp.exe` with `serve` as its argument. Reconnect the client after configuration changes. AMP-MCP uses stdio, not an HTTP port.

Run `amp-mcp.exe version` to check the executable. If Windows blocked the download, inspect its origin and checksum before using Windows' unblock option. Running `serve` manually waits for MCP messages; it is not an interactive console.

## No connections are listed

Run `amp-mcp.exe configure` and **Save** a connection; Test does not save. Check that the MCP client runs as the same Windows user and uses the same `AMP_MCP_CONFIG`, if set.

## Authentication or TLS fails

Use an HTTPS URL and an AMP **Service Login** token for ADS. A normal password is supported only for a bound native instance connection. Replace revoked tokens through the local manager.

Configure a private CA's PEM file or trust it in Windows. The certificate must match the URL hostname. TLS verification cannot be disabled.

DPAPI credentials cannot move to another PC or Windows account. Re-enter them there.

## ADS works, but the game says permission denied

ADS and applications have separate contexts. Check the exact node with `amp_check_permissions` and the application's `instance_id`. A common-role flag alone grants no application rights.

For a managed role, preview `amp_expand_privileges`. If `missing_actor_permissions` is nonempty, use an authorized bootstrap administrator. Role-edit permission does not permit granting rights the actor lacks. See [new modules](setup.md#full-admin-and-new-modules).

## Steam asks for verification or ownership

Some AMP templates download through Steam and require an account entitled to the game or dedicated server. AMP may display **Steam Login** even though AMP API authentication and permissions are correct.

Complete sign-in and verification in the AMP panel using an entitled account. Do not give the assistant your Steam password or Steam Guard code. AMP-MCP does not bypass ownership. Check the selected application's requirements and task result before retrying.

## An update was accepted, but the game is stopped

Check whether the response acknowledged submission or established completion. Inspect the job, application status, console and persisted log tail. A vanished task is `unknown`, not successful. Resolve Steam verification or other AMP prompts before another installation attempt.

`Running: true` means the AMP process is running. The game's `AppState` is separate: `-1` is shown as `Offline`, `0` as `Stopped` and `20` as `Ready`.

## Console history is empty

Console output contains messages received by the current MCP process. It cannot recover messages from before it connected. Use `tail` for its newest buffered messages or `amp_tail_file` for a persisted log. Inspect truncation and file-change fields.

If a game unexpectedly sleeps, check its AMP sleep/idle settings. Disable them only when continuous operation is intended.

## A write timed out or setup partially failed

Do not repeat a mutation blindly. Inspect `amp_get_job`, `amp_reconcile_job` or `amp_get_account_setup` using returned IDs. Account setup and chunked uploads are not atomic. Reconciliation reads state without replaying writes.

## Report a problem

Open an [issue](https://github.com/ShiroTsuma/AMP-MCP/issues) with the AMP-MCP version, AMP version, module, tool, reproduction steps and sanitized error. Remove tokens, passwords, session IDs, private addresses and raw private API responses.
