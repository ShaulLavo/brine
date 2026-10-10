# Brine

**Your apps. Your server. Under control.**

Agent-first self-hosted deployments, with a human-friendly Charm CLI.

An experiment for running personal applications on enrolled Linux servers. Routine deployments use a deterministic command API; the full terminal interface is still planned.

> **Status: stateless deployment implemented.** Brine supports enrollment, connected planning, detached deployment, app and operation status, logs, diagnosis, reconciliation, recovery resolution, configuration and secret changes, lifecycle plans, rollback and stateless removal. The [Phase 03 physical test](docs/evidence/p03-pi.md) passed on the authorized test host. This is not a production-readiness, zero-downtime or R2-restore claim. Persistent app data and Litestream/R2 backup and restore remain Phase 04 work. The Charm TUI is currently a welcome screen.

## Components

| Responsibility | Component | Current scope |
| --- | --- | --- |
| HTTPS / proxy | Caddy | Owned routing, validation, reload and health checks |
| Containers | Podman | Digest-pinned images and immutable secret versions |
| Service lifecycle | Quadlet + systemd | Owned app units, detached jobs and boot reconciliation |
| Control state | SQLite | Durable plans, releases and operation records |
| Persistent app databases | SQLite | Planned in Phase 04 |
| App backups | Litestream to S3-compatible storage, including R2 | Planned in Phase 04; no verified restore yet |
| Private administration | Tailscale | Operator-managed access, not a deployment prerequisite |
| Reference cloud host | Hetzner | Deployment is limited to enrolled Linux servers, not one provider |
| Human UI | Charm | Welcome screen now; full operation UI planned |

The reference server platform is Debian 13. The CLI can run on a laptop; deployment effects run on the enrolled server through restricted SSH credentials and operator policy.

## Run locally

Requires a recent Go toolchain.

~~~sh
go run ./cmd/brine version
go run ./cmd/brine doctor
go run ./cmd/brine doctor --json --no-input
go run ./cmd/brine tui
go run ./cmd/brine validate examples/offline/brine.toml --policy examples/offline/policy.toml
go run ./cmd/brine plan examples/offline/brine.toml --offline --snapshot examples/offline/fresh-host.json --policy examples/offline/policy.toml
go run ./cmd/brine plan examples/offline/brine.toml --offline --snapshot examples/offline/host-with-app.json --policy examples/offline/policy.toml --state examples/offline/brine-state.json
go test ./...
~~~

Offline plans are previews and cannot be applied. Connected plans are stored on the selected target and revalidated before execution.

## Agent workflow

Use `--json` for one response or `--jsonl` for supported event streams, with `--no-input` to forbid prompts. Keep the same `--target` and any explicit `--config-dir` throughout the workflow.

1. Run `plan` and inspect its `kind` and `conflicts`. A successful response can contain a conflict plan. Fix the input or host condition and plan again; do not apply a conflict plan.
2. Save an idempotency key **before** calling `apply PLAN_ID --idempotency-key KEY`. If the response is lost, the saved key identifies the same operation. Do not invent a new key to retry an uncertain mutation.
3. Acceptance is not completion. Save the returned operation ID and poll `status --operation OPERATION_ID`. Judge the recorded terminal outcome, not the acceptance response.
4. After an uncertain outcome, use read-only `diagnose` and `reconcile --dry-run` to inspect it. Reconciliation and supported `resolve` operations are separate recovery actions; do not blindly repeat the original mutation.
5. Treat app rollback and data recovery separately. App rollback never rewinds a database. Persistent data and R2 restore are not implemented yet.

Secret values enter through stdin, never command-line arguments. Secret ingestion is bounded and cancellable, including an unfinished pipe. Embedded CLI callers must lend stdin exclusively for the invocation. Files and in-memory readers are supported; other blocking readers must implement `ReadContext(context.Context, []byte) (int, error)`. Brine never closes a borrowed input stream.

The machine response contract is versioned and tested, but is not yet a stable public API. See [command contracts](docs/CONTRACTS.md) for response and exit semantics.

## Persistent preparation (Phase 04)

A new persistent app needs a separate approved allocation before credential
delivery, reviewed schema initialization and deployment:

~~~sh
brine data prepare brine.toml --target NAME --json
brine apply PREPARATION_PLAN_ID --target NAME --idempotency-key SAVED_KEY --json
~~~

The first command only saves a plan. It freezes the incarnation, database,
replica binding and epoch identities, private paths and backup credential scope.
Applying that plan provisions the pinned mapping-probe image, measures the
policy-authorized filesystem and UID/GID mapping, and records untouched private
allocations. It does not create SQLite files, deliver credentials, initialize or
migrate schemas, publish units, start writers or replication, or change routes.
Inventory and planning only observe existing allocations; missing data is not
proof of an empty database. An interrupted partial preparation is inspected,
never blindly replayed. The physical Phase 04 acceptance gates remain open.

## Next work

- Persistent SQLite app data, independently managed Litestream replication to operator-supplied S3-compatible storage and isolated restore drills.
- Separate, policy-gated application schema changes.
- A full Charm TUI sharing the command API's operation engine.
- Later drops, git previews and approved WebTransport support.

No destructive operation should bypass authorization boundaries, durable operation records or recovery tests.

## Build plan

Start with the [implementation roadmap](docs/PLAN.md), then the [phase-by-phase task list](docs/plans/README.md). These documents include [command contracts](docs/CONTRACTS.md), a [failure/acceptance test matrix](docs/TEST_MATRIX.md), and [agent instructions](AGENTS.md). Use completed task checkboxes and recorded evidence to distinguish implemented behavior from future work.

See also [Architecture](docs/ARCHITECTURE.md).

## License

No license has been chosen yet. Public visibility does not imply an open-source license.
