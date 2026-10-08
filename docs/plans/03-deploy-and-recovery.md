# Phase 03: Durable deployments and rollback

**Depends on:** successful runtime fixture. **Goal:** one application deployment can survive failures and client disconnection.

### Tasks

- [ ] **P03-01** Implement the durable plan/operation/release/event store in the runner's control database **on the target** (D1). Use a maintained SQLite Go driver chosen in its own dependency PR. Include migrations, append-only events, unique operation IDs, monotonic sequence numbers, serialized host mutation lock and idempotency keys. Test crashes between journal writes.
- [ ] **P03-02** Make `apply PLAN_ID` re-check target identity, stale observed generation, policy and image digest *after* acquiring the target mutation lock. Return refusal rather than silently generating a different deployment.
  Apply must retain the exact normalized desired input with the plan, verify its `DesiredHash` and the plan fingerprint under the host lock, and refuse before mutation on mismatch or absence; literal environment values are not in `Plan`.
- [ ] **P03-03** Run execution as a transient systemd user unit `brine-op-<operation-id>` running `brine host run-op` (D1), with durable target-side state. CLI disconnect must not stop the operation. Test the disconnect through the real dispatcher: record intent before launching the unit, and verify completion after systemd has garbage-collected it. Never expose arbitrary systemd command execution to the agent.
- [ ] **P03-04** Stage immutable release artifacts, pull/verify OCI digest, prepare existing-data mounts and managed routing. Switch the old writer off before starting the new one; accept controlled maintenance downtime. Do not accidentally run two SQLite writers.
- [ ] **P03-05** Implement bounded startup/direct HTTP health, routed health and explicit commit. On safe failure, restore previous release/unit/routing and prove its health; retain failed job state. Unknown schema/data compatibility => `recovery_required`.
- [ ] **P03-06** Implement `status`, `logs` and `rollback` planning. Logs must enforce app ownership, tail limits and secret redaction; rollback **cannot** rewrite SQLite data.
- [ ] **P03-07** Reconcile interruptions at every external effect boundary: killed job, lost SSH, unavailable Caddy, disk full, timed-out Podman call. Inspect real state before retrying; do not blindly replay partially completed steps.

### Exit gate

An authorized stateless test app deploys successfully. An invalid release never reports success; old release is either restored safely or the operation records a precise `recovery_required` state. Parallel applies conflict cleanly, and job outcomes remain observable after the CLI process exits.

**Evidence:** T04, T07-T11, T16-T17, T21-T22. Include automated fault-injection tests and one physical test-host disconnect/reboot drill; do not claim zero downtime.
