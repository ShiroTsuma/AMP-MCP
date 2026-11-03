# Development

AMP-MCP is a standalone Go stdio server using the official MCP Go SDK. Release targets are Windows x64 and Linux x64 (`CGO_ENABLED=0`, pure Go). Credentials use CurrentUser DPAPI on Windows and the Secret Service or an encrypted vault on Linux; connections are edited in a local browser panel.

## Build and verify

Install Go at the version in `go.mod` or later. Python 3 is needed only for the protocol smoke check.

```powershell
go build -o amp-mcp.exe .            # GOOS=linux CGO_ENABLED=0 go build -o amp-mcp .
go vet ./...
go test ./...
python scripts/smoke.py
```

Tests focus on protocol validation, authentication/delegation, credential storage (DPAPI, legacy PowerShell files, Linux vault), the connection panel, routing, file boundaries, uncertain operations and permission setup failures. Cover important risks rather than mirroring every endpoint.

To check that an existing (copied) connection file still opens, without contacting AMP or printing secrets, set `AMP_MCP_REAL_CONFIG=<copy of connections.json>` and run `go test -run TestRealConnectionsFileOpens .`.

Optional read-only live verification:

```powershell
python scripts/smoke.py --connection my-panel
```

Keep credentials, captured responses and artifacts outside the repository. Live writes require the server owner's authorization.

## Source layout

| Source | Responsibility |
| --- | --- |
| `main.go`, `catalog.json`, `validation.go` | MCP registration, curated tools and validation |
| `client.go`, `config.go`, `credentials.go`, `storage*.go` | HTTPS, scoped authentication, configuration, sealed credentials and atomic files |
| `manager.go` | Browser connection panel and generated-credential viewer |
| `internal/panel`, `internal/secretstore`, `internal/paths` | Local panel server, DPAPI/Secret Service/vault backends, per-OS directories (copied from the author's CodexTools common module) |
| `module_actions.go`, `settings.go` | Typed module discovery and setting metadata |
| `account_setup.go`, `expand_privileges.go`, `permissions.go` | Profiles, scope and permission maintenance |
| `jobs.go`, `status_codes.go` | Durable operations and status presentation |
| `files.go`, `install_file.go`, `tail_file.go` | Transfers, integrity and log tails |

## Contract conventions

Expose implemented workflows with typed bounded inputs and actionable errors. Return one representation through MCP content; do not duplicate structured output. Reserve stdout for MCP traffic. Discovery is not execution, and acknowledgement is not completion.

Preserve ADS/application boundaries. Never send ADS credentials as native child credentials, retry uncertain mutations or expose authentication IDs. Keep secrets in the sealed credential store; never render a stored secret back in the panel. Missing privileges require an already authorized actor.

## Release archives

Build the same package used by CI:

```powershell
.\scripts\package-release.ps1 -Version v0.9.0                    # Windows ZIP
./scripts/package-release.ps1 -Version v0.9.0 -Platform linux    # Linux tar.gz; run on Linux to keep the executable bit
```

Outputs are `dist/amp-mcp-windows-x64.zip` or `dist/amp-mcp-linux-x64.tar.gz`, plus a line in `dist/SHA256SUMS`. Packaging builds a trimmed binary, verifies its version when it runs on the target OS and uses an explicit file allowlist: executable, README, user guides and applicable license notices. Source, fixtures, development notes and configuration are excluded. The panel HTML and CSS are embedded. `scripts/check_release.py --platform windows|linux` verifies an archive on its own OS.

## GitHub Actions

- **CI** runs on pushes and pull requests on Windows and Ubuntu. Each checks formatting, runs vet, Go tests and the stdio smoke check, then builds and inspects its release archive, uploaded as a workflow artifact.
- **Release** runs on pushed `v*` tags. Windows and Ubuntu jobs validate and package; a publish job writes one `SHA256SUMS` for both archives and creates the GitHub release with generated notes. A prerelease suffix marks the release as a prerelease.

Commit and push reviewed source, then push a semantic version tag such as `v0.9.0` to publish. Actions must be enabled. The workflow uses the built-in `GITHUB_TOKEN` and needs no AMP credentials or personal token. Untagged CI runs do not publish.

Archive filenames are stable. Version identity comes from the tag and `amp-mcp.exe version`. Do not retag released versions. Remaining work is in the [roadmap](roadmap.md).
