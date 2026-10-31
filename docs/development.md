# Development

AMP-MCP is a standalone Go stdio server using the official MCP Go SDK. The release target is Windows x64: credentials use Windows CurrentUser DPAPI and the connection window uses Windows PowerShell.

## Build and verify

Install Go at the version in `go.mod` or later. Python 3 is needed only for the protocol smoke check.

```powershell
go build -o amp-mcp.exe .
go vet ./...
go test ./...
python scripts/smoke.py
```

Tests focus on protocol validation, authentication/delegation, DPAPI, routing, file boundaries, uncertain operations and permission setup failures. Cover important risks rather than mirroring every endpoint.

Optional read-only live verification:

```powershell
python scripts/smoke.py --connection my-panel
```

Keep credentials, captured responses and artifacts outside the repository. Live writes require the server owner's authorization.

## Source layout

| Source | Responsibility |
| --- | --- |
| `main.go`, `catalog.json`, `validation.go` | MCP registration, curated tools and validation |
| `client.go`, `config.go`, `credentials_*.go` | HTTPS, scoped authentication, configuration and DPAPI |
| `Connection-Manager.ps1`, `manager_*.go` | Embedded local connection manager |
| `module_actions.go`, `settings.go` | Typed module discovery and setting metadata |
| `account_setup.go`, `expand_privileges.go`, `permissions.go` | Profiles, scope and permission maintenance |
| `jobs.go`, `status_codes.go` | Durable operations and status presentation |
| `files.go`, `install_file.go`, `tail_file.go` | Transfers, integrity and log tails |

## Contract conventions

Expose implemented workflows with typed bounded inputs and actionable errors. Return one representation through MCP content; do not duplicate structured output. Reserve stdout for MCP traffic. Discovery is not execution, and acknowledgement is not completion.

Preserve ADS/application boundaries. Never send ADS credentials as native child credentials, retry uncertain mutations or expose authentication IDs. Keep secrets in DPAPI storage. Missing privileges require an already authorized actor.

## Release ZIP

Build the same package used by CI:

```powershell
.\scripts\package-release.ps1 -Version v0.1.0
```

Outputs are `dist/amp-mcp-windows-x64.zip` and `dist/SHA256SUMS`. Packaging builds a trimmed Windows x64 binary, verifies its version and uses an explicit file allowlist: executable, README, user guides and applicable license notices. Source, fixtures, development notes and configuration are excluded. The connection manager is embedded.

## GitHub Actions

- **CI** runs on pushes and pull requests using Windows. It checks formatting, runs vet, Go tests and the stdio smoke check, then builds and inspects the release ZIP. ZIP and checksum are uploaded as workflow artifacts.
- **Release** runs on pushed `v*` tags. It validates and packages before creating a GitHub release with ZIP, checksum and generated notes. A prerelease suffix marks the release as a prerelease.

Commit and push reviewed source, then push a semantic version tag such as `v0.1.0` to publish. Actions must be enabled. The workflow uses the built-in `GITHUB_TOKEN` and needs no AMP credentials or personal token. Untagged CI runs do not publish.

The ZIP filename is stable. Version identity comes from the tag and `amp-mcp.exe version`. Do not retag released versions. Remaining work is in the [roadmap](roadmap.md).
