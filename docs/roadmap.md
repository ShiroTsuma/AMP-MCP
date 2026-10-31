# Roadmap

Development backlog only. Existing workflows and operational limits are documented in [Using AMP-MCP](tools.md).

## Features

- [ ] Complete ADS onboarding from an authorized bootstrap connection: create the target account, provision a Service Login and save a verified connection without exposing credentials.
- [ ] Add curated backup listing, creation, restoration and deletion after verifying plugin contracts and restore requirements. Typed plugin discovery exists; dedicated backup tools do not.
- [ ] Upload an existing local artifact with bounded binary transfer.
- [ ] Validate scheduler arguments against each action's advertised specification.
- [ ] Add sanitized structured diagnostics for connection/capability failures.
- [ ] Provide local entry for required provisioning secrets; secret-requiring templates currently need the AMP panel.

## Verification

- [ ] Exercise the connection manager and generated-credential viewer interactively across supported Windows setups.
- [ ] Verify session termination with an AMP version supplying terminable session IDs.
- [ ] Expand live verification of non-ASCII paths, archive/plugin failures and interrupted transfers on disposable instances.
- [ ] Verify fresh full-admin onboarding end to end, including candidate-role authority before temporary bootstrap access is removed.
