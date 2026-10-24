# Working on Brine

Brine is an agent-first deployment CLI (Go, Cobra, Charm) for enrolled Linux servers. It drives Caddy, Podman/Quadlet, systemd, SQLite and Litestream/R2. [docs/status.md](docs/status.md) lists what is implemented. Read it before you trust any older wording.

## Start here

1. Read [decisions](docs/DECISIONS.md) and [contracts](docs/CONTRACTS.md). Decisions override older plan wording.
2. Read the roadmap `.internal/PLAN.md` and the phase file for your task in `.internal/plans/` (index: `.internal/plans/README.md`).
3. Inspect the **actual code**. Plans describe intent, and a checked task box means shipped behavior plus evidence.
4. Pick **one task ID** with completed dependencies. Implement and test it in a reviewable change.
5. Update task checkboxes only when behavior and acceptance evidence exist.

## Build principles

- One operation engine, two presentations: terminal UI and deterministic machine API.
- Keep Cobra and Charm out of domain packages. `tools/checkgate` enforces this and the rules in [CONTRIBUTING.md](CONTRIBUTING.md#rules-with-an-executable-check).
- Use typed subprocess arguments, timeouts, bounded output and structured errors. Never concatenate untrusted values into shell commands.
- Pin OCI image digests and dependency versions. Review major Charm updates together, not as incidental changes.
- Unit tests run without VPS access. Host integration tests require an **explicitly authorized disposable test host**. The owner's Raspberry Pi is authorized within the limits in [D3](docs/DECISIONS.md#d3-test-host-the-owners-raspberry-pi).
- Preserve unrelated edits. Never reset branches, force-push or rewrite history without authorization.
- Public repo: no credentials, .env files, private keys, real customer data, R2 secrets, or identifiable VPS inventories.

## Agent authority

This section governs coding agents building Brine. What enrolled runtime agents may do to a host through Brine is defined separately by the operator policy and [D8](docs/DECISIONS.md#d8-agents-manage-the-whole-lifecycle-through-brine-operations-only).

A request to build Brine **does not authorize** removing Coolify, Podman, Caddy, databases, app volumes, servers, Tailscale, DNS or firewalls. Do not run install/uninstall scripts on a real server without approval naming that target and action.

An agent's own `--yes` flag is not authorization. Routine agent credentials must not have unrestricted root SSH, raw Docker/Podman socket access, privileged Caddy admin access, or the ability to delete cloud resources. Document and test the boundary before autonomous production use.

When an operation's state is unknown after a timeout, inspect and reconcile it. Do not blindly retry irreversible actions. Database rollback and application rollback are separate.

## Verify

For code changes run at least:

```sh
go test ./...
go vet ./...
go build ./cmd/brine
git diff --check
```

`./scripts/check.sh` runs the full CI set. Add tests from `.internal/TEST_MATRIX.md` for the affected behavior. Report which checks ran and what was not verified. Mock tests are not evidence of a working deploy or a successful R2 restore.

## Docs traps

- `internal/result` tests (`TestDocumentedErrorCodes`) match exact rows of the error table in `docs/CONTRACTS.md`. Change a code's message, exit category or retry flag and the table row together.
- `docs/reference/commands.md` is generated from `--help`. After you add or change a command or flag, regenerate it with the command at the top of that file.
- Public docs and the README never link into `.internal/`. Only this file does.
