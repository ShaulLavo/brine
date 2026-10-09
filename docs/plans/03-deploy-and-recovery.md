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

- [ ] **P03-08** Config operations per D8: `config set` (environment, resources, domains, health), `secret set` (stdin, new immutable version, bound only by a plan) and `restart`/`stop`/`start`, each a plan through the same apply path under the host lock, journaled and reconcilable. Domain changes reuse the planner's ownership and policy checks; there is no second permissive path. Depends on P03-01 to P03-05.
- [ ] **P03-09** `remove APP` for stateless apps per D8: plan and apply that stops the app and removes its unit and route under the host lock. Persistent-data archival is P04-08. Depends on P03-01 to P03-05.
- [ ] **P03-10** (depends on P03-01, P03-06 and P02-01) `diagnose [APP]`: one read-only report, built for agents, combining status, health, recent operations and diffs, a bounded redacted log tail, Caddy routing state and host resources, with plain-language findings and suggested next operations.

### Approved follow-up for P03-02: installed-app listener ownership

The installed-image fix verifies actual running image facts, but live update/no-op planning has a second independent blocker. `internal/inventory/listeners.go` records listener ports, processes and cgroup units without ever assigning `target.PortOwner.App`. `internal/plan/plan.go` requires an app-owned listener for an existing allocation, so the authorized disposable fixture returned exactly `[{Code:port_owned Field:host_port}]` from its no-op assertion on 2026-10-09. The image, unit, secret, live port and Caddy receipt checks had already passed. This happened once in the one live prepare run; cleanup passed and left the enrollment with no apps. No reboot or update apply ran.

Reproduce by building `go test -tags pi_integration -c ./integration`, then running the resulting binary as the enrolled runner on an explicitly authorized disposable host with `-test.v -test.run '^TestPiFixture$' -fixture-stage prepare`. `fixture.checkInstalledPlans` provides the assertion and committed-release fixture. On failure inspect the persisted fixture receipt before cleanup; run the same binary with `-fixture-stage cleanup` rather than retrying prepare. Locally, `go test ./internal/inventory -run TestInstalledImageObservationAndPlanning -count=1` proves installed image observations permit no-op, environment-update and image-update plans when listener ownership is supplied by the existing planner fixture. Do not treat those fake listener owners as live evidence.

Bind listener ownership to verified Brine container/service observations without assigning unrelated or ambiguous sockets to an app. Add failing-first tests for genuine owned listeners, foreign listeners on the same port, missing process/cgroup facts and rootless forwarding processes. Re-run the live installed-app planner assertion without replacing `PortOwners`. Keep the default Caddy catch-all refusal; the adapter fixture deliberately scopes only its authorized route when testing plans. This is required before claiming live installed-app updates work.

### Exit gate

An authorized stateless test app deploys successfully. An invalid release never reports success; old release is either restored safely or the operation records a precise `recovery_required` state. Parallel applies conflict cleanly, and job outcomes remain observable after the CLI process exits.

**Evidence:** T04, T07-T11, T16-T17, T21-T22. Include automated fault-injection tests and one physical test-host disconnect/reboot drill; do not claim zero downtime.
