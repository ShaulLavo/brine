# Brine architecture

## Goal

One modest VPS deployment system operated by humans and coding agents. Prefer dependable existing software over a custom container runtime or a miniature PaaS.

## Layers

~~~text
Human (Charm TUI)       Agent (JSON/non-interactive)
         \                  /
                  Go CLI
          plan / apply / status
                    |
             systemd / Quadlet
                    |
                Podman apps
                    |
             host Caddy proxy

  App SQLite files -- independent Litestream --> R2
  Administration -- Tailscale --> Hetzner VPS
~~~

Caddy, Podman, Litestream, systemd, and Tailscale are **candidates for integration**, not deployed by this repository today. Rust components are welcome when there is a concrete reason to write them.

## Guardrails

- **Plan before apply**: inspect and diff intended changes. Never infer approval from a flag an unprivileged agent can set.
- **No default root access** for routine agent operations. Keep destructive host operations behind separate credentials/approval.
- **Machine-readable behavior**: JSON stdout, diagnostics stderr, stable exit codes, operation IDs, no prompts in --no-input mode.
- **Data survives releases**: persistent app volumes and the replication service must be isolated from deploy and rollback.
- **Rollbacks**: app image rollback is not a database rewind. Database restore is an explicit separate operation.
- **Backups**: R2 replication is asynchronous. Test restores into isolated directories and avoid duplicate replicators targeting the same path.
- **Honest availability**: zero downtime is a future capability, not something Quadlet or Caddy grants automatically.

## First deployment test

Deploy a disposable web app with SQLite, confirm TLS/proxy routing, simulate a failed release, reboot the VPS, recover the application, and restore a backup to an isolated location. Measure deploy time and memory before claiming performance advantages.

## Non-goals at bootstrap

Multi-server orchestration, a custom container engine, Kubernetes, a web dashboard, and autonomous host deletion.
