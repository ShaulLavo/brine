# Working on Brine

Brine is an **agent-first deployment CLI** with a beautiful Charm human interface. It integrates Caddy, Podman/Quadlet, systemd, SQLite and Litestream/R2.

## Start here

1. Read [the roadmap](docs/PLAN.md), [decisions](docs/DECISIONS.md) and [contracts](docs/CONTRACTS.md).
2. Read the relevant phase file in [docs/plans](docs/plans/README.md).
3. Inspect the **actual code**, not just planning documents. Initially only `version`, a PATH-only `doctor`, and a welcome-screen `tui` exist.
4. Pick **one task ID** with completed dependencies. Implement and test it in a reviewable change.
5. Update task checkboxes only when behavior and acceptance evidence exist.

## Build principles

- One operation engine, two presentations: terminal UI and deterministic machine API.
- Keep Cobra and Charm out of domain packages.
- Use typed subprocess arguments, timeouts, bounded output and structured errors. Never concatenate untrusted values into shell commands.
- Pin OCI image digests and dependency versions. Review major Charm updates together, not as incidental changes.
- Unit tests run without VPS access. Host integration tests require an **explicitly authorized disposable test host**; the owner's Raspberry Pi is authorized within the limits in [D3](docs/DECISIONS.md).
- Preserve unrelated edits. Never reset branches, force-push or rewrite history without authorization.
- Public repo: no credentials, .env files, private keys, real customer data, R2 secrets, or identifiable VPS inventories.

## Very important: agent authority

A request to build Brine **does not authorize** removing Coolify, Podman, Caddy, databases, app volumes, servers, Tailscale, DNS or firewalls. Do not run install/uninstall scripts on a real server without approval naming that target and action.

An agent's own `--yes` flag is not authorization. Routine agent credentials must not have unrestricted root SSH, raw Docker/Podman socket access, privileged Caddy admin access, or the ability to delete cloud resources. Explicitly document and test the boundary before autonomous production use.

When an operation's state is unknown after a timeout, inspect/reconcile it. Do not blindly retry irreversible actions. Database rollback and application rollback are separate.

## Evidence

For code changes run at least:

~~~sh
go test ./...
go vet ./...
go build ./cmd/brine
git diff --check
~~~

Add tests from [TEST_MATRIX.md](docs/TEST_MATRIX.md) for the affected behavior. Report which checks ran and what was not verified. Mock tests are not evidence of a working deploy or successful R2 restore.
