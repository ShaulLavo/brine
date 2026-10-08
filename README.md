# Brine

**Your apps. Your server. Under control.**

Agent-first self-hosted deployments, with a human-friendly Charm CLI.

A Go + Rust-friendly experiment for running personal applications on a VPS. The CLI uses [Cobra](https://github.com/spf13/cobra), [Bubble Tea](https://github.com/charmbracelet/bubbletea), and [Lip Gloss](https://github.com/charmbracelet/lipgloss). The goal is to make routine deployments deterministic for coding agents while still feeling good in a terminal.

> **Status: bootstrap.** This repository contains a working CLI shell, version reporting, a local tool check, and a TUI welcome screen. It **cannot deploy applications yet**.

## Proposed components

| Responsibility | Proposed component |
| --- | --- |
| HTTPS / proxy | Caddy (Go) |
| Containers | Podman (Go) |
| Service lifecycle | Quadlet + systemd |
| App databases | SQLite |
| Backups | Litestream to Cloudflare R2 |
| Private administration | Tailscale |
| Cloud host | Hetzner |
| Orchestration & human UI | Go + Charm |
| Specialized helpers | Rust, only when they earn their place |

The project is **Go + Rust**, not a requirement to rewrite reliable existing tools for language purity.

## Run locally

Requires a recent Go toolchain.

~~~sh
go run ./cmd/brine version
go run ./cmd/brine doctor
go run ./cmd/brine doctor --json --no-input
go run ./cmd/brine tui
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

See [Architecture](docs/ARCHITECTURE.md).

## License

No license has been chosen yet. Public visibility does not imply an open-source license.
