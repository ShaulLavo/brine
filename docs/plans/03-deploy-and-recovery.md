# Phase 03: Durable deployments and rollback

**Depends on:** successful runtime fixture. **Goal:** one application deployment can survive failures and client disconnection.

### Tasks

- [ ] **P03-01** Implement the durable plan/operation/release/event store in the runner's control database **on the target** (D1). Use a maintained SQLite Go driver chosen in its own dependency PR. Include migrations, append-only events, unique operation IDs, monotonic sequence numbers, serialized host mutation lock and idempotency keys. Test crashes between journal writes.
- [ ] **P03-02** Make `apply PLAN_ID` re-check target identity, stale observed generation, policy and image digest *after* acquiring the target mutation lock. Return refusal rather than silently generating a different deployment.
  Apply must retain the exact normalized desired input with the plan, verify its `DesiredHash` and the plan fingerprint under the host lock, and refuse before mutation on mismatch or absence; literal environment values are not in `Plan`.
- [ ] **P03-03** Run execution as a transient systemd user unit `brine-op-<operation-id>` running `brine host run-op` (D1), with durable target-side state. CLI disconnect must not stop the operation. Test the disconnect through the real dispatcher: record intent before launching the unit, and verify completion after systemd has garbage-collected it. Never expose arbitrary systemd command execution to the agent.
- [ ] **P03-04** Stage immutable release artifacts, pull/verify OCI digest, prepare existing-data mounts and managed routing. Switch the old writer off before starting the new one; accept controlled maintenance downtime. Do not accidentally run two SQLite writers.
  Unit evidence covers the stateless executor in `internal/apply`: both image digests, immutable secret versions, isolated Quadlet validation, stop-before-install/start, and validated Caddy publication. Quiescence requires both an inactive user unit and a stopped or absent Podman container. Existing-data mounts remain unchecked until P04-01 defines them in the typed desired input and renderer. Real-host evidence comes with the Pi apply run later in this wave.
- [x] **P03-05** Implement bounded startup/direct HTTP health, routed health and explicit commit. On safe failure, restore previous release/unit/routing and prove its health; retain failed job state. Unknown schema/data compatibility => `recovery_required`.
  Unit evidence covers deadline-bound direct and every-domain routed probes, explicit commit/read-back, reverse rollback, old-release health proof, and failures at every forward and rollback effect boundary. The schema-v1 typed input is affirmatively stateless; a frozen-shape compile guard forces P04 to classify new data fields explicitly. Unclassified data requires positive compatibility evidence. Journal intent/outcome failures and unknown reload outcomes stop further effects. The store and job runner are separate tasks; no deploy, disconnect, reboot, or data-recovery result is claimed here. Real-host evidence comes with the Pi apply run later in this wave.
- [ ] **P03-06** Implement `status`, `logs` and `rollback` planning. Logs must enforce app ownership, tail limits and secret redaction; rollback **cannot** rewrite SQLite data.
  - [x] `logs`: read-only ownership-checked journal tails, typed arguments, line/byte/time limits, pattern redaction, human/JSON/JSONL client output. Evidence includes argv, ownership/injection refusals, cap and redaction regressions, planted-secret fuzzing, and a scrubbed Debian 13 journal fixture. See the bounded app logs contract for the real-host fixture limitation.
  - [ ] `status` and `rollback` planning remain separate work.
- [ ] **P03-07** Reconcile interruptions at every external effect boundary: killed job, lost SSH, unavailable Caddy, disk full, timed-out Podman call. Inspect real state before retrying; do not blindly replay partially completed steps.

- [ ] **P03-08** Config operations per D8: `config set` (environment, resources, domains, health), `secret set` (stdin, new immutable version, bound only by a plan) and `restart`/`stop`/`start`, each a plan through the same apply path under the host lock, journaled and reconcilable. Domain changes reuse the planner's ownership and policy checks; there is no second permissive path. Depends on P03-01 to P03-05.
- [ ] **P03-09** `remove APP` for stateless apps per D8: plan and apply that stops the app and removes its unit and route under the host lock. Persistent-data archival is P04-08. Depends on P03-01 to P03-05.
- [ ] **P03-10** (depends on P03-01, P03-06 and P02-01) `diagnose [APP]`: one read-only report, built for agents, combining status, health, recent operations and diffs, a bounded redacted log tail, Caddy routing state and host resources, with plain-language findings and suggested next operations.

### Exit gate

An authorized stateless test app deploys successfully. An invalid release never reports success; old release is either restored safely or the operation records a precise `recovery_required` state. Parallel applies conflict cleanly, and job outcomes remain observable after the CLI process exits.

**Evidence:** T04, T07-T11, T16-T17, T21-T22. Include automated fault-injection tests and one physical test-host disconnect/reboot drill; do not claim zero downtime.
