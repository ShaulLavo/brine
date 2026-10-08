# Brine implementation roadmap

**Status: planning document.** No deployment functionality is implemented by writing this file.

**Product goal:** a small, fast, trustworthy self-hosted deployment tool on a single Linux VPS, operated either by an AI agent using stable JSON or by a person using Charm.

## Current baseline

The existing repo is a Go/Cobra starter with Bubble Tea/Lip Gloss welcome UI, JSON version output, PATH-only doctor and basic CI. It cannot yet deploy, discover remote servers, plan changes, persist operations, configure Caddy, restore SQLite or manage containers. Charm dependencies are currently Bubble Tea **v1**, not a completed v2 migration.

## Default architecture

~~~text
        Human: Brine TUI          Agent: Brine --json
                 \                  /
                   Go operation API
                plans / policy / state
                           |
              explicitly enrolled host
             SSH / restricted dispatcher
                           |
       +-------------------+-----------------+
       |                   |                 |
 Podman + Quadlet    Caddy (host)     Litestream (systemd)
       |                   |                 |
   OCI app release   HTTPS / routing     SQLite --> R2
       |
  persistent SQLite volumes
~~~

Existing **Hetzner + Tailscale** remain in place; no automatic teardown/reprovisioning. Go owns CLI/orchestration; Rust stays optional for a distinct component with demonstrated value. Use standard, replaceable tools instead of building a container engine or reverse proxy.

## Decided MVP limits

- One explicitly enrolled, compatible Linux/systemd host; one trusted operator; one web app container per app.
- Prebuilt OCI images pinned by digest; build in CI or locally, not as an implicit VPS mutation.
- Rootless Podman with Quadlet/systemd user services; Caddy runs independently on the host.
- Managed deploys accept brief maintenance downtime. **Zero downtime is not claimed.**
- One mutation at a time per host; read-only status/logs may run concurrently.
- Versioned app configuration, durable operation journal and release history.
- SQLite app data lives outside releases, and Litestream replication is independently managed.
- Agent mode requires stable, noninteractive structured output and limited credentials.
- No dashboard, multi-server orchestration, Kubernetes, OpenTofu/Ansible requirement, custom OCI engine, or automatic cloud resource deletion in v1.

## Implementation phases

| Phase | Plan | Deliverable | Exit criterion |
| --- | --- | --- | --- |
| 00 | [CLI foundation](plans/00-cli-foundation.md) | Testable command wiring and output contracts; evaluate Charm v2 together | JSON and no-input behavior fully testable |
| 01 | [App spec and planner](plans/01-spec-and-planner.md) | Strict `brine.toml`, deterministic dry-run plans | No host changes from validate/plan; unsafe values rejected |
| 02 | [Host and runtime](plans/02-host-and-runtime.md) | Target enrollment, Podman/Quadlet and Caddy adapters | A disposable fixture starts, routes and survives reboot |
| 03 | [Deploy/recover](plans/03-deploy-and-recovery.md) | Persistent jobs, health checks, rollback and reconcile | Failed deployment or dead SSH session is recoverable |
| 04 | [SQLite/R2](plans/04-sqlite-and-backups.md) | Persistent data and testable backup/restore workflows | Restore isolated R2 copy with SQLite integrity checks |
| 05 | [Charm and agents](plans/05-charm-and-agents.md) | Usable TUI and stable machine workflows calling same API | Equivalent operations and outcomes across both UIs |
| 06 | [Release hardening](plans/06-release-and-ops.md) | Policy gates, disaster drills, binaries and runbooks | End-to-end checklist passes on reference host |

**Start with P00-01.** Phases are intended as small, reviewable pull requests, not a single mega-implementation. TUI styling can be prototyped against fake data after phase 00, but must not be represented as a working deployment engine.

## Release milestones

**Milestone A, read-only planning:** phases 00-01. Useful locally, no production VPS needed.

**Milestone B, disposable alpha:** phases 02-03. Deploy real fixtures to an authorized test host, never private data.

**Milestone C, personal apps:** phases 04-05 plus the production blockers in phase 06. Genuine restore/reboot/rollback and privilege-boundary tests required.

## Definition of done

A checked task has implementation, tests, relevant documentation, and the exact acceptance evidence requested. A plan can fail if assumptions about OS versions or third-party integration prove false; record a short architecture decision and update downstream tasks rather than pretending compatibility.

Every mutation identifies the target, verifies ownership, journals intent before external effects, and returns a traceable operation ID. Destructive infrastructure activity is out of scope. Application rollback **must not** roll back databases. See [contracts](CONTRACTS.md), [test matrix](TEST_MATRIX.md), [architecture](ARCHITECTURE.md), and [agent instructions](../AGENTS.md).
