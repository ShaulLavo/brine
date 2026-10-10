# Phase plans

Start with [the roadmap](../PLAN.md) and [decisions](../DECISIONS.md), then read [contracts](../CONTRACTS.md) and [the test matrix](../TEST_MATRIX.md). **None of these tasks are complete merely because the plan was written.** Check items only when implemented, tested, and documented.

| Order | File | Focus |
| --- | --- | --- |
| 00 | [CLI foundation](00-cli-foundation.md) | Testable commands, structured output, Charm baseline |
| 01 | [Specification and planning](01-spec-and-planner.md) | Strict app definition, deterministic plans |
| 02 | [Runtime integration](02-host-and-runtime.md) | Enrolled host, Quadlet, Caddy |
| 03 | [Deploy and recovery](03-deploy-and-recovery.md) | Durable jobs, release/rollback |
| 04 | [SQLite and R2](04-sqlite-and-backups.md) | Persistence and verified recovery |
| 05 | [Charm and agents](05-charm-and-agents.md) | Full TUI and reliable JSON interface |
| 06 | [Release and operations](06-release-and-ops.md) | Permission boundaries, drills, distribution |
| 07 | [Drops and previews](07-drops-and-previews.md) | Temporary sites, short links, git previews |
| 08 | [WebTransport apps](08-webtransport.md) | App-terminated QUIC/UDP first; shared-port ingress and authorized previews gated separately |
| 09 | [Managed SpacetimeDB](09-spacetimedb.md) | SQLite-preserving engine boundary, standalone service, fenced backups, reviewed publish and Pi drill |
| 10 | [Easy app data](10-easy-data.md) | Environment contract, schema files in the app repo, init without sudo, one-command first deploy |

## Prompt for an implementation agent

> Read AGENTS.md, docs/PLAN.md, docs/DECISIONS.md, docs/CONTRACTS.md, docs/TEST_MATRIX.md and the specific phase plan. Inspect the current code. Implement only TASK-ID plus truly necessary prerequisites. Do not deploy or change any VPS, DNS, firewall, credentials, database or unrelated service. Preserve existing work; add tests, run checks, and report limitations. Check off a task only after its acceptance criteria are actually verified.

Use one small pull request per task where practical. Phase 00 first. Never auto-grant deploy, root or teardown privileges to the agent.
