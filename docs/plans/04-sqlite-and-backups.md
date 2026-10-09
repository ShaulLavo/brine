# Phase 04: Persistent SQLite and R2 recovery

**Depends on:** working disposable deployment. **Goal:** app code changes never endanger database directories, and a real R2 restore is demonstrable.

### Tasks

- [ ] **P04-01** Define persistent per-app data directories, ownership, permissions, storage quotas and mount lifecycle outside release directories. Ensure cleanup/rollback never deletes them.
- [ ] **P04-02** Design one independent Litestream systemd service per SQLite database with **one active replicator per replica destination**. Ensure proper DB/WAL permissions and safe restart ordering; do not assume read-only bind mounts work.
- [ ] **P04-03** Store scoped Cloudflare R2 credentials in a runner-owned 0600 file per destination, loaded by the Litestream user unit (D5). Never expose secrets to the app image, CLI stdout, logs, state store or public repo. Validate selected Litestream binary/config version.
- [ ] **P04-04** Add read-only backup status, lag/degraded health, R2 credential/network failure reporting and operational runbooks. Do not equate “replicator process started” with “data safely stored”.
- [ ] **P04-05** Implement `restore test` into isolated, nonpublic data/fixture paths. Never write into the live DB. Run SQLite `PRAGMA integrity_check`, check a known fixture row and report recovered timestamp/lag when supported by pinned tool versions.
- [ ] **P04-06** Test application replacement, reboot, proxy outage, R2 outage, missing credentials, backup retention and competing replicators. Write a separate operator-approved **live restore runbook**, not an autonomous command.
- [ ] **P04-07** Separate app migration policy from deployment: reject implicit unreviewed migrations; require an explicit migration plan, a backup/restore point and compatible rollback semantics before considering migration automation.

- [ ] **P04-08** Persistent removal, retention and purge per D8 (needs P04-01 to P04-04 and P03-09). Removal quiesces writers and replicator and archives data under an immutable archive ID bound to its recorded data path and backup destinations. Retention runs 30 days from commit with a restorable backup set throughout, and removal refuses if an R2 lifecycle rule or running replicator would break that. Expiry and `data purge ARCHIVE_ID` act by ID only, refuse live or reused paths and destinations, and purge needs `allow_agent_purge = true`. Tests: refusal by default, the retention boundary, remove-recreate-expire, active replication, and interruption during archive and purge (T22).
- [ ] **P04-09** `restore live` per D8 (needs P04-05 and P04-06): a planned, policy-gated (`allow_agent_live_restore`, default false) replacement of an app's database from a chosen backup. It takes a fresh restore point first, quiesces writers and replicator, integrity-checks the restored copy before swap-in, and recovers after interruption. Until this ships, live restore stays the P04-06 operator runbook.

### Exit gate

Known writes survive a new app release and reboot. A real isolated restore from R2 passes database integrity/invariant checks. Live data was never touched during the restore. Recovery-point limits and failure modes are documented.

**Evidence:** T12-T16, T19, T22-T23. Asynchronous replication **cannot guarantee zero loss of last writes**; add periodic verified restore drills.
