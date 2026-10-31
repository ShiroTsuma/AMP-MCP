# Account and connection setup

AMP-MCP runs on your Windows PC and calls AMP over HTTPS. The AMP host needs no additional MCP software.

## Choose your access

Use a dedicated local AMP account. Existing accounts with suitable permissions can connect directly; profile setup is optional.

| Profile | Allowed operations |
| --- | --- |
| `observer` | View application status and console output |
| `operator` | Manage game lifecycle and updates, console commands, game files, module settings and actions; no AMP account/role administration |
| `admin` | Full administration in the selected scope, including files, settings, accounts and roles |

| Scope | Effect |
| --- | --- |
| `global` | A template/common role in ADS, shared with existing and future instances |
| `instance` | A native role inside one application, without ADS administration or access to other instances |

Profiles are game-agnostic. Local roles exist inside the application, not in ADS's global role list. For ADS users, local setup also grants entry to that application on the user's personal ADS role.

### Role capabilities

| Operation in the selected application scope | Observer | Operator | Admin |
| --- | --- | --- | --- |
| View status and console output | Yes | Yes | Yes |
| Start, stop, restart and update the game | No | Yes | Yes |
| Send console commands | No | Yes | Yes |
| Browse, upload, edit and remove game files; install mods | No | Yes | Yes |
| Configure the game module and execute its actions | No | Yes | Yes |
| Manage AMP users, roles and their permissions | No | No | Yes |
| Change AMP panel settings or edit AMP configuration files | No | No | Yes |

Operator is the game/module manager. Admin adds AMP administration within the selected scope. Game lifecycle access does not give an operator ADS rights to create/delete instances or start/stop their AMP processes.

Scope is independent of the profile: a global operator manages applications across instances; a local operator manages only the selected instance. A local admin can manage AMP accounts and roles inside that instance, but does not become an ADS administrator. Global admin includes ADS administration.

Operator permissions are resolved from the selected module's advertised catalog, including module settings and actions, plus game-file operations. They do not use an unrestricted application wildcard. A new module or newly advertised setting can require an explicit expansion by an authorized administrator.

## Connect an existing account

1. Create a dedicated enabled local user and assign its intended role in AMP. For full administration, an AMP administrator can assign **Super Admins** to the dedicated account. For a separately managed profile, use the workflow below.
2. Log in as that user and create a **Service Login** in AMP's account interface.
3. Run `amp-mcp.exe configure` from the release folder.
4. Enter the connection name, main ADS HTTPS URL, username and Service Login token. Leave **Instance ID** empty and **Authentication** set to `service_login` for a usual ADS connection.
5. Select **Test**, then **Save**. Test does not save the form.
6. Register the executable with the `serve` argument in your MCP client, as shown in the [README](../README.md#quick-start).

Permission to edit roles alone is **not sufficient** to bootstrap administration. AMP also checks authority to grant the requested rights, create roles and change memberships. Preview reports missing authority before changes.

## Agent-assisted permission setup

You need two named connections to the same ADS panel:

- A bootstrap administrator that already holds the authority it will grant.
- The dedicated target account's own Service Login connection.

Create the target account and Service Login first. It should have no extra roles apart from its personal role or one dedicated temporary bootstrap role. Do not use a protected built-in role as the removable bootstrap role. Automatic ADS account and Service Login creation are not included in profile setup.

Shared roles require AMP's licensed SharedRoles feature. The example application AMP process must run; its game can be stopped. Full global admin needs exactly one application example. Other global profiles use one example per selected module type. Local setup takes exactly its target instance.

Once both connections are saved, replace the placeholders in this prompt:

```text
Use AMP-MCP to configure existing target connection <target-connection>
using <bootstrap-connection>. I authorize creating a new role named
<role-name> with profile <observer|operator|admin> and scope <global|instance>.
Use application <instance-guid> as the setup target/example.

List permission profiles and preview account setup. Inspect the grants
and missing bootstrap authority. If authority is missing, report the exact
requirement; do not promote accounts or bypass AMP permissions.
Apply the current plan_id, then verify fresh target access.
Global finalization needs a newly created disposable verification instance:
get my authorization before creating that fixture. Local finalization uses
the selected instance. Remove a temporary target membership only if I have
identified its dedicated bootstrap_role_id.
Report setup_id and any partial failure without replaying mutations.
```

### Setup tool sequence

1. `amp_list_permission_profiles` lists profiles.
2. `amp_preview_account_setup` validates the target, scope, grants and bootstrap authority. For example:

   ```json
   {
     "connection": "bootstrap-admin",
     "target_connection": "service-account",
     "profile": "operator",
     "scope": "instance",
     "role_name": "Server operator",
     "application_instance_ids": ["<APPLICATION_INSTANCE_GUID>"]
   }
   ```

3. `amp_setup_account` takes the same fields plus the returned `plan_id`. It creates the role, grants permissions, assigns the target and verifies a fresh login. Relevant changes invalidate the preview.
4. `amp_finalize_account_setup` takes `connection`, `setup_id` and `verification_instance_ids`. Local scope verifies the original instance. Global scope needs newly created instances for the configured modules; full admin accepts one new application of any module. Creating verification instances is a separate operation.

An optional `bootstrap_role_id` identifies a dedicated temporary target role. Finalization checks access before removing that membership and reauthenticates afterward. It does not remove other accounts' memberships. Local verification also checks denial of ADS administration and access to other known instances.

Setup is not atomic. Inspect `amp_get_account_setup` and `amp_get_job` after a partial failure before further writes. A failed final check does not silently restore privileges.

### Full admin and new modules

Full admin uses ADS namespace grants such as `Core.*`, `ADS.*` and `Settings.*`, plus application-scoped `*` through the API. AMP can display the shared application grant internally as `*.*`. This is intentional full authority. A bare `*` submitted in ADS does not replace ADS namespace grants.

Application authority covers new game settings without adding each setting separately. A historical snapshot of explicit permission leaves does not provide that future authority. Observer retains read-only Core rights. Operator resolves exact advertised module settings/actions and game-file rights; it excludes AMP user/role administration, panel settings and editing AMP configuration files. Operators are trusted to manage game files and executable game code.

To upgrade an existing operator role or initialize a newly supported module/setting, use the expansion workflow; profiles are not silently reapplied to saved roles. For a role managed by account setup, preview `amp_expand_privileges` with `connection`, an example `instance_id`, its exact module name and `mode: "preview"`. Supply the original `setup_id` if it cannot be uniquely resolved. Preview returns missing permissions, missing actor authority, denials to clear and `plan_id`. Apply that plan with `mode: "apply"`.

For an operator, use an administrator connection as `connection` and provide the operator's original `setup_id`. The operator cannot expand its own role because it has no role-editing authority. Preview/apply adds the rights now defined by the operator profile without changing it to admin or widening its instance scope. Roles created manually, outside account setup, have no managed setup receipt and need explicit permission changes by an administrator.

Expansion preserves profile and scope. A local role cannot expand elsewhere. The actor must already hold the authority it grants. Expansion does not promote accounts, repair ADS authority or run automatically after an authorization error. A new ADS namespace requires an authorized administrator to configure it separately.

## Native instance accounts

An application can have users absent from ADS. To connect one:

1. Create its user and instance-specific role through the application's tools, then assign the intended rights.
2. Call `amp_generate_user_password` for that username and instance. It sets a generated password and saves it encrypted locally, returning `credential_path`.
3. Call `amp_create_user_connection` with the source `connection`, `instance_id`, `username`, that `credential_path` and `new_connection`.

The helper verifies native login and saves a password connection bound to the instance. It creates no permanent Service Login and refuses overwrites. Passwords do not pass through MCP. This is separate from ADS profile setup.

To view a generated credential yourself, run `amp-mcp.exe show-user-password PATH` locally. Do not copy its contents into chat.

## Connection storage and TLS

Default storage is `%LOCALAPPDATA%\AMP-MCP`. Windows CurrentUser DPAPI binds encrypted credentials to the Windows user and machine that saved them.

| Item | Purpose |
| --- | --- |
| `connections.json` | Named connections and encrypted credentials |
| `jobs.json` | Operation summaries, without original arguments or authentication IDs |
| `setups/` | Account setup and expansion receipts |
| `credentials/` | Encrypted generated credentials |
| `artifacts/` | Downloaded files |

Set `AMP_MCP_CONFIG` in the MCP process environment to use another connection file. Jobs, receipts, credentials and artifacts live beside it. Keep the directory private; downloaded artifacts can contain sensitive server data.

HTTPS verification is always enabled. Use a certificate trusted by Windows or set **CA certificate** to a PEM CA file. The URL hostname must match the certificate. Request timeout is configurable from 1 to 300 seconds; the manager's draft connection test has a separate 12-second cap.

See CubeCoders' [permission guide](https://discourse.cubecoders.com/t/managing-user-permissions-in-amp/2301) and [template role documentation](https://discourse.cubecoders.com/t/configuring-amp-for-enterprise-or-advanced-usage/1830) for AMP role concepts.
