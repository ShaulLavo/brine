# Phase 04: Persistent SQLite and R2 recovery

**Depends on:** working disposable deployment. **Goal:** app code changes never endanger database directories, and a real R2 restore is demonstrable.

### Tasks

- [ ] **P04-01** Define persistent per-app data directories, ownership, permissions, storage quotas and mount lifecycle outside release directories. Ensure cleanup/rollback never deletes them.
- [ ] **P04-02** Design one independent Litestream systemd service per SQLite database with **one active replicator per replica destination**. Ensure proper DB/WAL permissions and safe restart ordering; do not assume read-only bind mounts work.
- [ ] **P04-03** Implement target-owned backup secret references and scoped Cloudflare R2 credentials. Never expose secrets to the app image, CLI stdout, logs, state store or public repo. Validate selected Litestream binary/config version.
- [ ] **P04-04** Add read-only backup status, lag/degraded health, R2 credential/network failure reporting and operational runbooks. Do not equate “replicator process started” with “data safely stored”.
- [ ] **P04-05** Implement `restore test` into isolated, nonpublic data/fixture paths. Never write into the live DB. Run SQLite `PRAGMA integrity_check`, check a known fixture row and report recovered timestamp/lag when supported by pinned tool versions.
- [ ] **P04-06** Test application replacement, reboot, proxy outage, R2 outage, missing credentials, backup retention and competing replicators. Write a separate operator-approved **live restore runbook**, not an autonomous command.
- [ ] **P04-07** Separate app migration policy from deployment: reject implicit unreviewed migrations; require an explicit migration plan, a backup/restore point and compatible rollback semantics before considering migration automation.

### Exit gate

Known writes survive a new app release and reboot. A real isolated restore from R2 passes database integrity/invariant checks. Live data was never touched during the restore. Recovery-point limits and failure modes are documented.

**Evidence:** T12-T16, T19, T22-T23. Asynchronous replication **cannot guarantee zero loss of last writes**; add periodic verified restore drills.
