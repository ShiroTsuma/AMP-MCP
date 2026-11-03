<h1 align="center">AMP-MCP</h1>

<p align="center">
  <strong>Manage your AMP game servers from your AI assistant.</strong><br>
  A local MCP server for CubeCoders AMP on Windows and Linux, with encrypted credentials.
</p>

<p align="center">
  <a href="https://github.com/ShiroTsuma/AMP-MCP/releases/latest"><img src="https://img.shields.io/badge/Download-Windows_x64_%7C_Linux_x64-8ab4f8?style=flat-square" alt="Download for Windows x64 or Linux x64"></a>
  <a href="https://github.com/ShiroTsuma/AMP-MCP/actions/workflows/ci.yml"><img src="https://github.com/ShiroTsuma/AMP-MCP/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/Transport-stdio-98c379?style=flat-square" alt="stdio transport">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-98c379?style=flat-square" alt="MIT license"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#automatic-agent-setup">Automatic agent setup</a> ·
  <a href="docs/setup.md">Accounts and permissions</a> ·
  <a href="docs/troubleshooting.md">Troubleshooting</a>
</p>

Ask your assistant to inspect a server, change game settings, install mods, or create an instance. AMP-MCP connects to your existing AMP panel over HTTPS and exposes its operations as tools. Credentials stay on your machine, encrypted for your account (Windows DPAPI, or the Linux keyring / an encrypted vault).

## What you can do

| Task | Capabilities |
| --- | --- |
| Manage instances | Discover applications, create and clone instances, manage their lifecycle and network settings |
| Operate game servers | Read status and console output, send commands, start, stop, restart and update |
| Configure games | Discover module settings, available versions and typed game/plugin actions |
| Work with files | Browse, read, write, download and install files from HTTPS URLs with SHA-512 verification |
| Manage access | Create users and roles; use observer, operator or admin profiles globally or for one instance |
| Automate operations | Manage schedules and deployment templates; follow submitted operations through durable jobs |

Available operations depend on your AMP version, installed modules, license and account permissions. An admin account gives the assistant administrative access, including file and account changes.

## Quick start

**Requirements:** Windows x64 or Linux x64, a web browser for the local configuration panel, a client supporting local stdio MCP servers, and an AMP panel reachable over HTTPS. The AMP host can run Linux or Windows. The archive includes the executable; Go and Python are not needed to use it.

1. Download **`amp-mcp-windows-x64.zip`** (Windows) or **`amp-mcp-linux-x64.tar.gz`** (Linux) and **`SHA256SUMS`** from [Releases](https://github.com/ShiroTsuma/AMP-MCP/releases/latest). Verify the checksum (`Get-FileHash .\amp-mcp-windows-x64.zip -Algorithm SHA256` on Windows, `sha256sum -c --ignore-missing SHA256SUMS` on Linux), then extract the archive to a permanent folder.
2. In AMP, create a dedicated local account, assign the permissions you want the assistant to use, and create a **Service Login** while logged in as that account. See [account setup](docs/setup.md) for full administration or restricted roles.
3. Open a terminal in the extracted folder and run (`./amp-mcp configure` on Linux):

   ```powershell
   .\amp-mcp.exe configure
   ```

   This opens a local configuration page in your browser (the one-time link is also printed). Add a named connection such as `my-panel`, enter the panel's HTTPS URL, username and Service Login token, then **Test** and **Save**. A combined `username:token` is accepted. Enter credentials only in this local page; press **Close panel** when done.
4. Register the executable in your MCP client. For clients using `mcpServers` JSON:

   ```json
   {
     "mcpServers": {
       "amp-mcp": {
         "command": "C:\\Tools\\AMP-MCP\\amp-mcp.exe",
         "args": ["serve"]
       }
     }
   }
   ```

   Replace the path with your extracted location (for example `/opt/amp-mcp/amp-mcp` on Linux). Use your client's equivalent server configuration if its format differs, then reconnect or restart it.
5. Ask your assistant:

   > List my AMP connections, test `my-panel`, and show its instances and application status. Only read information.

For a local connection check, run `.\amp-mcp.exe test-connection my-panel`.

## Automatic agent setup

Paste this prompt into an agent that can download files, run local commands and edit your MCP client's configuration:

```text
Set up AMP-MCP from https://github.com/ShiroTsuma/AMP-MCP on this computer.
Read the repository README and docs/setup.md first. Ask only for missing
details: my MCP client, permanent install folder, AMP HTTPS URL, connection
name and intended access level.

Download the latest release archive for this OS (Windows x64 ZIP or Linux
x64 tar.gz) and SHA256SUMS from that repository's GitHub release. Verify the
checksum before extracting it.
Use the prebuilt executable; no source build or development tools are needed.
If there is no release, report that instead of downloading an unrelated build.

Run amp-mcp configure (or call amp_open_connection_manager) and give me the
local panel link so I can enter my username and Service Login token there. Do not ask me to paste credentials into chat or put them in
commands, MCP configuration or logs. Explain any required AMP-side account
and permission steps using docs/setup.md. I will handle sign-in and MFA.

Register the server as amp-mcp with args ["serve"] using my client's supported
configuration format. Preserve all existing servers and settings. Tell me
if the client must reconnect before its new tools become available.

After configuration, test the named connection and, when tools are available,
call amp_list_connections and amp_instance_read with operation list. Only perform read-only
verification. Report what succeeded and any remaining manual step; do not
create instances, change roles or start games as an installation test.
```

The agent can install and register the server. AMP sign-in, token entry, certificate trust and game-platform verification may still require you. To create a restricted role afterward, use the [permission setup prompt](docs/setup.md#agent-assisted-permission-setup).

## Example workflow

Start with an inventory request before making changes:

> List the instances on `my-panel`. For each instance, show its name, application and running state. Only read information.

The assistant discovers the connection, reads the instance inventory and summarizes it. Follow up using the returned instance identifier, for example: “Show the latest console output for this instance.” Names and available operations come from your AMP panel; no example credentials or server addresses are required.

## Example requests

- “Show stopped servers and the latest console messages for this instance.”
- “Discover this game's settings and show available server versions.”
- “Create a Fabric server, then install compatible mods from their publishers.”
- “Create an operator role for this instance with console commands, game files and module configuration.”
- “Check whether that update completed; don't submit it again.”

Game downloads can require a licensed Steam account or manual verification in AMP. A submitted job does not mean the game is installed or running.

## Updates and local data

Close MCP clients using the executable, replace the application files with the new release, then reconnect. Configuration and generated credentials live separately under `%LOCALAPPDATA%\AMP-MCP`.

Windows DPAPI credentials belong to the Windows user and machine that saved them; Linux credentials live in the keyring or the passphrase-protected `secrets.vault`. Re-enter them when moving to another PC, OS or account. Existing Windows connections from earlier releases keep working without migration. See [local data and TLS](docs/setup.md#connection-storage-and-tls) for custom configuration paths and CA certificates.

## Documentation

| Guide | Contents |
| --- | --- |
| [Setup](docs/setup.md) | Accounts, Service Logins, permission profiles, native users and TLS |
| [Using the tools](docs/tools.md) | Scope, game settings, module actions, files, console and jobs |
| [Troubleshooting](docs/troubleshooting.md) | Authentication, permissions, Steam verification and incomplete operations |
| [Development](https://github.com/ShiroTsuma/AMP-MCP/blob/master/docs/development.md) | Build, validation and release automation |
| [Roadmap](https://github.com/ShiroTsuma/AMP-MCP/blob/master/docs/roadmap.md) | Remaining work and verification |

Report bugs in [GitHub Issues](https://github.com/ShiroTsuma/AMP-MCP/issues). Include the AMP-MCP version, AMP version, affected module and sanitized error. Remove credentials and private server details from attachments.

AMP-MCP is an independent project and is not affiliated with CubeCoders.

Released under the [MIT License](LICENSE). Release archives include third-party license notices.
