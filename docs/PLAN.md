# Brine implementation roadmap

**Status: Approved.** No deployment functionality is implemented by writing this file. Architecture decisions live in [DECISIONS.md](DECISIONS.md) and take precedence over older wording here.

**Product goal:** Vercel-fast deploys on your own server (D9): a small, fast, trustworthy self-hosted deployment tool on a single Linux VPS that an AI agent can run end to end (deploy, change, troubleshoot, remove and maintain, per D8) using stable JSON, with a person able to do the same through Charm.

**Scope: your own Linux servers only** (owner, 2026-10-10). Brine deploys to hosts it enrolls: rootless Podman through Quadlet and systemd, Caddy in front, SQLite with Litestream, plus approved managed SpacetimeDB work in D13/Phase 09. Managed platforms such as Cloudflare Workers, Pages, D1, managed object-storage buckets, KV or Durable Objects are not Brine targets. They use their own declarative tooling (for Cloudflare, Wrangler and `wrangler.toml`). A future layer above Brine and Wrangler may present both; it is a separate project. Using S3-compatible object storage as an engine backup destination is in scope, for example Cloudflare R2. Brine accepts externally supplied scoped credentials; provider-specific credential minting and APIs are outside Brine. Phase 07 should reconsider hosting static drops on Cloudflare instead of the VPS. The main day-to-day workflow will be generic production plus per-PR preview deployments for any repository, public or tailnet-only per site (see Phase 07).

**Trust model: a personal deployment service** (owner, 2026-10-10). Brine deploys code written by the owner and the owner's agents. It is not a multi-tenant host: strangers never upload code to it. Viewers of public sites and drops are untrusted *visitors*, but every deployed artifact comes from the owner or an agent using the owner's scoped credentials. Design isolation for that: protect the host from mistakes and from compromised dependencies, keep agents inside their policy, and keep apps from reading each other's secrets and data. Multi-tenant hardening for hostile uploaders (e.g. per-tenant sandboxes, hostile-code egress spikes) is not a goal; Phase 07 should simplify accordingly.

## Current baseline

The Go/Cobra CLI implements validation, offline and connected planning, enrollment, detached stateless deployment through Podman/Quadlet/systemd and Caddy, a SQLite control store with durable operation records, app and operation status, logs, diagnosis, reconciliation, supported terminal recovery resolution, configuration and secret changes, lifecycle plans, rollback and stateless removal. The [Phase 03 physical exit gate](evidence/p03-pi.md) passed. The welcome TUI uses Bubble Tea **v2** and Lip Gloss **v2**; the full operation UI remains later work. Persistent app data, Litestream backup/restore and schema-change operations are not shipped. No production-readiness, zero-downtime or verified R2-restore claim is made.

## Default architecture

~~~text
        Human: Brine TUI          Agent: Brine --json
                 \                  /
                   Go operation API
                           |
                OpenSSH (client only)
                           |
              explicitly enrolled host
     brine host serve (restricted dispatcher)
       control DB: plans / releases / operations
                           |
       +-------------------+-----------------+
       |                   |                 |
 Podman + Quadlet    Caddy (host)     Litestream (systemd)
       |                   |                 |
   OCI app release   HTTPS / routing     SQLite --> S3-compatible storage
       |
  persistent SQLite volumes
~~~

Existing **Hetzner + Tailscale** remain in place; no automatic teardown/reprovisioning. The reference OS is Debian 13 (amd64 or arm64), and the owner's Raspberry Pi is the disposable test host (D2, D3). Use standard, replaceable tools instead of building a container engine or reverse proxy.

## Decided MVP limits

- One explicitly enrolled, compatible Linux/systemd host; one trusted operator; one web app container per app.
- Prebuilt OCI images pinned by digest; build in CI or locally, not as an implicit VPS mutation.
- Rootless Podman with Quadlet/systemd user services; Caddy runs independently on the host.
- Managed deploys accept brief maintenance downtime. **Zero downtime is not claimed.**
- One mutation at a time per host; read-only status/logs may run concurrently.
- Versioned app configuration, durable operation journal and release history.
- Managed data lives outside releases. SQLite uses independent Litestream replication; Phase 09 adds a separately managed SpacetimeDB engine with fenced scheduled captures.
- Agent mode requires stable, noninteractive structured output and limited credentials.
- No dashboard, multi-server orchestration, Kubernetes, OpenTofu/Ansible requirement, custom OCI engine, or automatic cloud resource deletion in v1.

## Implementation phases

| Phase | Plan | Deliverable | Exit criterion |
| --- | --- | --- | --- |
| 00 | [CLI foundation](plans/00-cli-foundation.md) | Testable command wiring and output contracts; Charm v2 migration | JSON and no-input behavior fully testable |
| 01 | [App spec and planner](plans/01-spec-and-planner.md) | Strict `brine.toml`, deterministic dry-run plans | No host changes from validate/plan; unsafe values rejected |
| 02 | [Host and runtime](plans/02-host-and-runtime.md) | Target enrollment, Podman/Quadlet and Caddy adapters | A disposable fixture starts, routes and survives reboot |
| 03 | [Deploy/recover](plans/03-deploy-and-recovery.md) | Persistent jobs, health checks, rollback and reconcile | Failed deployment or dead SSH session is recoverable |
| 04 | [SQLite/S3](plans/04-sqlite-and-backups.md) | Persistent data and testable backup/restore workflows | Restore isolated S3-compatible storage copy with SQLite integrity checks |
| 05 | [Charm and agents](plans/05-charm-and-agents.md) | Usable TUI and stable machine workflows calling same API | Equivalent operations and outcomes across both UIs |
| 06 | [Release hardening](plans/06-release-and-ops.md) | Policy gates, disaster drills, binaries and runbooks | End-to-end checklist passes on reference host |
| 07 | [Drops and previews](plans/07-drops-and-previews.md) | Temporary websites with short links, git branch/PR previews (D9) | One-command drop with pill controls; pull request gets a preview URL |
| 08 | [WebTransport apps](plans/08-webtransport.md) | Policy-approved UDP publishing, app TLS delivery/ownership and HTTP-plus-UDP readiness (D10); optional protocol health | UDP mapping/reachability, certificate lifecycle and recovery on both architectures; shared UDP 443 and previews gated separately |
| 09 | [Managed SpacetimeDB](plans/09-spacetimedb.md) | Shared engine boundary, standalone service, reviewed module publish and stopped-tree S3 backups (D13) | Published amd64/arm64 fixture, Pi/browser drill, verified remote restore and no SQLite regression |
| 10 | [Easy app data](plans/10-easy-data.md) | `BRINE_DB_*` environment contract, schema files in the app repo, reviewed init without manual install, one-command first deploy | New app with a database deploys with one command; custom SQLite builds pass restore tests |

Phase 10 makes data easy to use without an SDK: apps read `BRINE_DB_*` variables and keep schema files in their repo. P10-01 to P10-03 run before P09-04 so SpacetimeDB shares the same contract.

Phase 09 begins with a behavior-preserving engine boundary after the merged Phase 04 foundations. Runtime probes and the multiarch fixture precede engine admission; exact remote restore precedes module publishing and live replacement. Production scope remains the owner's enrolled servers with externally supplied scoped S3 credentials. The pinned SpacetimeDB license limits an application/service to one production instance unless alternative rights are recorded.

Phase 08 research starts while Phase 03 finishes. Its initial full-app implementation follows Phase 03's physical exit gate and can run beside Phase 04. Shared-port ingress coordinates with P07-01; WebTransport drops/previews wait for both phases' authorization and isolation gates. See Phase 08 for the unmerged Caddy session-proxy blocker and the direct-UDP first delivery.

**Start with P00-01.** Phases are intended as small, reviewable pull requests, not a single mega-implementation. TUI styling can be prototyped against fake data after phase 00, but must not be represented as a working deployment engine.

## Release milestones

**Milestone A, read-only planning:** phases 00-01. Useful locally, no production VPS needed.

**Milestone B, disposable alpha:** phases 02-03. Deploy real fixtures to an authorized test host, never private data.

**Milestone C, personal apps:** phases 04-05 plus the production blockers in phase 06. Genuine restore/reboot/rollback and privilege-boundary tests required.

## Definition of done

A checked task has implementation, tests, relevant documentation, and the exact acceptance evidence requested. A plan can fail if assumptions about OS versions or third-party integration prove false; record a short architecture decision and update downstream tasks rather than pretending compatibility.

Every mutation identifies the target, verifies ownership, journals intent before external effects, and returns a traceable operation ID. Destructive infrastructure activity is out of scope. Application rollback **must not** roll back databases. See [contracts](CONTRACTS.md), [test matrix](TEST_MATRIX.md), [architecture](ARCHITECTURE.md), and [agent instructions](../AGENTS.md).
