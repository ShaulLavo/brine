# Brine architecture

## Goal

One modest deployment system operated by humans and coding agents on enrolled Linux servers. Prefer dependable existing software over a custom container runtime or a miniature PaaS.

## Implemented layers

~~~text
Human CLI / welcome TUI       Agent JSON / non-interactive CLI
             \                         /
                       Go CLI client
                restricted SSH dispatcher
                             |
         plans / apply / status / diagnose / recovery
                             |
          SQLite control state and operation journal
                             |
                 systemd jobs / Quadlet
                             |
                        Podman apps
                             |
                    host Caddy proxy
~~~

Caddy, Podman, Quadlet/systemd and the SQLite control store are integrated. Deployment effects run on the enrolled host, not the laptop. Routine client credentials use restricted SSH dispatch and operator policy rather than unrestricted root access. Debian 13 is the reference server platform; Hetzner and Tailscale are operator choices, not mandatory providers or transport prerequisites.

The CLI implements stateless planning and deployment, release and operation status, bounded logs, read-only diagnosis, reconciliation, supported terminal recovery resolution, environment/secret and lifecycle plans, rollback and stateless removal. The [Phase 03 physical evidence](evidence/p03-pi.md) records deployment, rollback, interrupted-operation recovery, reboot and final removal. Those drills do not prove production readiness or zero downtime.

## Planned layers

Phase 04 adds persistent app SQLite data and independent Litestream replication to operator-supplied S3-compatible storage such as R2, with isolated restore and archival paths. The SQLite **control store** already exists; persistent **app data** support does not. No successful R2 restore is claimed. Application schema changes are separate from deployment ([D12](DECISIONS.md#d12-app-schema-changes-are-separate-from-deployment)). The full Charm operation UI is later presentation work; today's TUI is a welcome screen.

## Guardrails

- **Plan before apply**. Inspect plan kind, conflicts and intended changes. A conflict plan is a successful planning result, not permission to apply. Never infer approval from a flag an unprivileged agent can set.
- **No default root access** for routine agent operations. Keep destructive host operations behind separate credentials and operator policy.
- **Machine-readable behavior**. JSON stdout, diagnostics stderr, stable exit categories, operation IDs and no prompts in `--no-input` mode.
- **Acceptance is not completion**. Save the idempotency key before mutation and poll the returned operation ID. Diagnose and reconcile uncertain outcomes before retrying.
- **Target context stays explicit**. Follow-up commands retain the selected target and explicit configuration directory.
- **Data is separate from releases**. Persistent data isolation is Phase 04 work; stateless deployment must not imply it is implemented.
- **Rollbacks**. App image rollback is not a database rewind. Database restore is an explicit separate operation.
- **Backups**. Planned R2 replication is asynchronous. Test restores into isolated directories and avoid duplicate replicators targeting the same path.
- **Honest availability**. Zero downtime is a future capability, not something Quadlet or Caddy grants automatically.

## Next physical acceptance test

After Phase 04 implementation, deploy a disposable app with SQLite, verify data survives release changes and reboot, and restore its R2 backup into an isolated location. Measure deploy time and memory before claiming performance advantages. This persistence drill has not passed yet.

## Non-goals

Multi-server orchestration, a custom container engine, Kubernetes, a web dashboard, and creating or deleting cloud machines.
