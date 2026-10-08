# Brine

**Your apps. Your server. Under control.**

Agent-first self-hosted deployments, with a human-friendly Charm CLI.

An experiment for running personal applications on a VPS. The goal is to make routine deployments deterministic for coding agents while still feeling good in a terminal.

> **Status: bootstrap.** This repository contains a working CLI shell, version reporting, a local tool check, and a TUI welcome screen. It **cannot deploy applications yet**.

## Components

| Responsibility | Component (chosen, not yet integrated) |
| --- | --- |
| HTTPS / proxy | Caddy |
| Containers | Podman |
| Service lifecycle | Quadlet + systemd |
| App databases | SQLite |
| Backups | Litestream to Cloudflare R2 |
| Private administration | Tailscale |
| Cloud host | Hetzner |
| Orchestration & human UI | Brine CLI with a Charm terminal UI |

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

**The JSON contract is a starting point**, not yet a stable public API.

## Direction

1. Declarative app configuration and a read-only deployment plan.
2. Versioned releases and Podman/Quadlet integration.
3. Caddy routing, health checks, safe traffic switching, and rollback.
4. Persistent SQLite data with independently managed Litestream backups.
5. Structured operation IDs, output, audits, and scoped permissions for agents.
6. A full Charm TUI sharing the same underlying operations.

No destructive commands should be implemented without authorization boundaries, durable operation records, and recovery tests.

## Build plan

Start with the [implementation roadmap](docs/PLAN.md), then the [phase-by-phase task list](docs/plans/README.md). These documents include [command contracts](docs/CONTRACTS.md), a [failure/acceptance test matrix](docs/TEST_MATRIX.md), and [agent instructions](AGENTS.md). Plans describe **future behavior**, not features already implemented.

See also [Architecture](docs/ARCHITECTURE.md).

## License

No license has been chosen yet. Public visibility does not imply an open-source license.
