# Phase 06: Release security and operations

**Depends on:** real deployment, backup recovery, human/agent interfaces. **Goal:** graduate from a personal experiment to an intentionally supported single-host tool.

### Tasks

- [ ] **P06-01** Prove a server-side **policy/authorization boundary** owned by the operator, not the agent: restricted command dispatch, pinned policy, target ownership, separate credentials, forbidden arbitrary shell/Podman/Caddy/host teardown. Test bypass attempts.
- [ ] **P06-02** Build preflight/readiness command that checks exact OS/runtime versions, disk capacity, DNS/TLS prerequisites, rootless systemd user session, control DB health, image registry access and backup status, without mutating the host.
- [ ] **P06-03** Ship signed, versioned binaries and reproducible release metadata for supported platforms. Document upgrade/downgrade compatibility and backup the Brine control database.
- [ ] **P06-04** Run disaster exercises: unhealthy release, Caddy drift, systemd restart, VPS reboot, lost connection, failed partial write, target disk full, and restore from actual R2 into a fresh isolated environment.
- [ ] **P06-05** Document human-safe runbooks for credential rotation, app rollback, service ownership, manual Caddy repair, control DB recovery, off-host backups and **approved** live SQLite restoration.
- [ ] **P06-06** Check limits/security: a single VPS is not HA, app trust boundaries are limited, and R2 backups are asynchronous. Removed-app data expires automatically after 30 days, while early purge and live restore stay denied unless policy allows them (D8). Provide a threat model and disclosure checklist.
- [ ] **P06-07** Publish a minimal quickstart for an **explicitly enrolled disposable host**, clear CLI examples and supported-version matrix. Choose a project license deliberately.

- [ ] **P06-08** Host operations per D8 through the root-owned helper: `host update`, `host restart-caddy`, `host cleanup` and `host reboot`. The helper authorizes independently against root-owned policy and enrollment records, takes fixed verbs only, and is safe against runner-writable files and symlinks. Update plans bind exact package transactions and declare service and app impact. Every verb takes the host lock. Reboot records intent durably and reconciles after boot. The runner's polkit rule stays reload-only. Evidence: update failure, service-restart impact, a concurrent deploy refused during a reboot or update, post-boot health, and T18 direct-helper, forged-record and symlink bypass tests.

### Exit gate

Every production-blocking case in [TEST_MATRIX.md](../TEST_MATRIX.md) is backed by redacted results from an authorized reference host and restore drill. Machine errors are reliable, partial states are inspectable, and the agent cannot bypass policy simply by bypassing Brine's client.

**Evidence:** T05-T24 as relevant, plus reproducible CI artifacts. Without a separately enforced authorization boundary or a verified restore, describe Brine as *experimental*, not production ready.
