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

Persistent application data has separate engine-specific operations. SQLite uses
declared app-owned files and independent Litestream replication. Managed
SpacetimeDB uses a separate pinned standalone service, private immutable roles,
online native snapshot/log captures and encrypted remote storage. Exact data
operations stop writers under a durable fence and consume a fresh prepared point.
Reviewed publication and live restore verify privately, preserve the original
tree and activate a new stopped epoch. A fresh managed deployment starts it.
The [SpacetimeDB runbook](runbooks/spacetimedb.md) describes the public commands;
the [physical evidence](evidence/p09-pi.md) records first publication, Pi browser
execution and online real R2 replay on both architectures. Changed-module, live
restore, archive, assigned faults and final delivery remain incomplete.

## Planned layers

The full Charm operation UI remains later presentation work; today's TUI is a
welcome screen. Phase 04 and Phase 09 still require their shared physical
recovery and failure drills. Implemented commands do not establish successful
R2 recovery or phase completion. Application schema changes remain separate from
deployment ([D12](DECISIONS.md#d12-app-schema-changes-are-separate-from-deployment)).

## Guardrails

- **Plan before apply**. Inspect plan kind, conflicts and intended changes. A conflict plan is a successful planning result, not permission to apply. Never infer approval from a flag an unprivileged agent can set.
- **No default root access** for routine agent operations. Keep destructive host operations behind separate credentials and operator policy.
- **Machine-readable behavior**. JSON stdout, diagnostics stderr, stable exit categories, operation IDs and no prompts in `--no-input` mode.
- **Acceptance is not completion**. Save the idempotency key before mutation and poll the returned operation ID. Diagnose and reconcile uncertain outcomes before retrying.
- **Target context stays explicit**. Follow-up commands retain the selected target and explicit configuration directory.
- **Data is separate from releases**. Declared app-owned data, schema changes and live restore have independent identities and authority.
- **Rollbacks**. App image rollback is not a database rewind. Database restore is an explicit separate operation.
- **Backups**. SQLite replication is asynchronous. SpacetimeDB captures online native history at the admitted cadence. Test remote recovery privately; reported cadence does not establish zero loss.
- **Honest availability**. Zero downtime is a future capability, not something Quadlet or Caddy grants automatically.

## Next physical acceptance test

Complete the shared SQLite recovery gate and the
[Phase 09 Pi/browser drill](plans/09-spacetimedb.md#p09-08-run-the-physical-pi-and-browser-recovery-drill).
Reuse the earned ordinary online capture/replay core on arm64 and amd64 from
[the physical evidence](evidence/p09-pi.md). Complete compatible code/module
changes, live restore, held-fence reboot/interruption and archive retention.
Record remaining resource measurements, final installed merged binaries and
actual-host teardown. The phase exit remains pending.

## Non-goals

Multi-server orchestration, a custom container engine, Kubernetes, a web dashboard, and creating or deleting cloud machines.
