# Phase 08: Deploy a game server over UDP

**Status: Approved**, owner, 2026-10-13. Deliver this before transport previews.
This plan owns P08-02, P08-03, P08-04, P08-06 and P08-08. Their checkboxes
live here. [WebTransport extensions](08-webtransport.md) retain the other tasks.

**Goal:** deploy a digest-pinned game server with its ordinary HTTP endpoint
and one direct UDP port. The game owns its protocol, authentication and any
QUIC certificates. Brine owns admission, publishing, observations and recovery.

## Scope and dependencies

Use the existing enrolled host, rootless Podman, Quadlet, host lock, journal,
HTTP health and Caddy generation transaction. Each app gets a distinct approved
UDP allocation. Accept the existing maintenance downtime during replacement.

Start after the Phase 02 runtime and Phase 03 deployment/recovery foundations.
P08-01's completed protocol experiment is useful evidence, not a prerequisite
for a protocol-independent UDP declaration. Do not wait for Phase 07 previews,
Phase 09 SpacetimeDB completion, the full TUI, or a Caddy WebTransport proxy.
Keep existing SQLite and managed-engine permits and compatibility checks intact.
Adding UDP never initializes, migrates, restores or rolls back a database.

This delivery excludes shared UDP 443, transport gateways, preview authorization,
WebTransport probes and browser interoperability. It also excludes certificate
issuance and new Brine-managed TLS delivery. App-owned QUIC remains possible;
the game supplies its own valid certificate and trusted browser hash delivery
when using that mode. Optional Brine TLS delivery is P08-11.

No task authorizes DNS, firewall, Tailscale, ACL or production-host changes.
The operator supplies the approved network path. D3 covers disposable Pi drills;
use another host only under explicit authorization naming its target and actions.

## Planned declaration and output

Add one optional strict `[udp]` declaration beside the existing HTTP fields.
The following fragment is a design target, not accepted by today's parser:

```toml
[udp]
container_port = 7777
exposure = "tailnet"
```

Accept only `tailnet` or `public` exposure, explicitly admitted by operator
policy for that app. Policy supplies exact bind addresses, the client address
and an unprivileged host-port range. The app cannot choose a host address,
host port, network mode or raw Podman option. UDP admission defaults to denied.
Keep the existing HTTP port and health declaration mandatory for this delivery.

Represent allocations by protocol, address family, bind address, host port,
container port and immutable app incarnation. Bind these facts and policy
identity into desired input, plans, releases, reservations and rollback evidence.
Do not retain a numeric-port-only representation for UDP ownership decisions.

Return the approved client address, allocated port, protocol and exposure in
plan review and committed status. Return separate local readiness, remote
reachability and protocol-verification facts. Local readiness requires HTTP
health and the exact owned UDP mapping/listener. Remote reachability stays
unknown without a real client-vantage probe. Brine supplies no generic UDP
health packet and claims no game session or browser WebTransport verification.

## Current implementation and edit boundaries

Written against `6c9eb48`. Recheck these paths before implementation:

- `internal/spec/spec.go` has one `ContainerPort` and HTTP `Health`.
- `internal/policy/policy.go` has one `AppPorts` range. Add explicit UDP policy.
- `internal/quadlet/render.go` emits loopback TCP `PublishPort` without `/udp`.
- `internal/target/snapshot.go` and `internal/inventory/listeners.go` retain
  numeric occupancy and TCP ownership. Extend protocol-specific observations.
- `internal/plan`, `internal/store` and `internal/host` carry desired input,
  reservations and release facts. Update their real callers in the same change.
- `internal/apply/health.go` performs HTTP probes. Keep that behavior and add
  UDP ownership evidence without implementing a game protocol.
- `internal/apply/compatibility.go` delegates persistent compatibility. Preserve
  that delegation and managed-engine behavior during UDP lifecycle work.

Update CLI, JSON, diagnosis and offline fixtures with their domain changes.
Keep Cobra and Charm out of domain packages. Keep existing HTTP routing and
unrelated listeners intact. Do not add compatibility paths for old binaries.

## Execute the tasks

- [ ] **P08-02** Add strict UDP declarations, policy and deterministic plans.
  Extend canonical hashes, offline snapshots, configuration diffs, immutable
  desired input, store migrations and reservations. Keep denied plans free of
  host effects. Bind freshness to admitted UDP ranges and retained allocations,
  preserving the current port-admission projection's treatment of unrelated
  client churn. Recheck collisions and drift under the mutation lock.
  **Acceptance:** denied exposure, unknown fields, privileged ports and unknown
  ownership refuse. Overlapping IPv4, IPv6 and wildcard binds conflict. Unknown
  dual-stack behavior refuses. TCP and UDP may share a number only with proved
  independent ownership. Concurrent plans cannot claim one UDP allocation.
  **Evidence:** T26/T28 and existing T02-T05/T17/T23. Run parser/policy tables,
  deterministic plan goldens, stale-plan cases and real store reservation tests.

- [ ] **P08-03** Publish the admitted UDP mapping through rootless Quadlet.
  Render an explicit approved address and `/udp` alongside the existing TCP
  mapping. Verify actual Podman mapping, network mode and owned unit/container
  identity. Use existing application shutdown bounds. Give the app no extra
  capabilities, host networking, host sockets or ingress credentials.
  **Acceptance:** generated and actual mappings match on both supported
  architectures. Unexpected bind, protocol or mapping refuses readiness.
  **Evidence:** T27/T28, renderer/argv tests and a real rootless runtime test.
  Record unavailable runtime evidence as unverified, not a mock success.

- [ ] **P08-04** Inventory UDP ownership and report readiness honestly.
  Retain address family, protocol, bind overlap and independent process,
  unit/container and mapping provenance. A port number or TCP owner cannot
  explain a UDP socket. Unknown owner, missing observation and incomplete
  rootless forwarding evidence refuse ready. Keep stopped-app reservations
  without claiming a live listener. Expose the same facts in status/diagnosis.
  **Acceptance:** HTTP success with a missing or foreign UDP mapping is not
  ready. An owned local UDP socket does not imply remote reachability. A
  client probe records its vantage, timestamp and actual result separately.
  **Evidence:** T28/T29, collector-to-planner tests and readiness/output goldens.

- [ ] **P08-06** Connect UDP to apply, rollback, removal and recovery.
  Depends on P08-02 through P08-04 and the Phase 03 physical gate. Stage the
  mapping before stopping the old writer. Start one replacement and commit
  only after HTTP health and UDP ownership pass. Rollback restores the previous
  mapping and reservation. Removal proves owned runtime/mapping absence before
  releasing the reservation. Route persistent removal through its existing
  archive authority; UDP never grants deletion of data.
  **Acceptance:** failure, SSH disconnect, SIGKILL and reboot at each new effect
  boundary preserve journaled ownership. Unknown effects are inspected before
  resolution. No port leak, duplicate writer, foreign-listener cleanup or
  database rewind occurs. Existing HTTP-only and managed-engine tests pass.
  **Evidence:** T30 and T08-T11/T16/T21-T23, including real subprocess kills.

- [ ] **P08-08** Prove deployment and remote UDP on disposable fixtures.
  Depends on P08-06. Use a small non-root HTTP-health plus bounded UDP-echo
  fixture with pinned dependencies/base and published immutable image digests.
  The echo is test evidence, not a required production game interface. Record
  actual arm64 and amd64 rootless runtime results. Run two allocations, update,
  failed health, rollback, disconnect, effect interruption, reboot and removal.
  Exchange a nonce from a separate client through the approved published port.
  Bound its read-only attempts and record the network vantage. Exercise a
  controlled unreachable client without changing host network policy.
  **Acceptance:** the expected release replies remotely, the previous release
  survives a failed replacement and owned UDP resources disappear after remove.
  Missing external evidence stays unknown. Persistent/managed data and unrelated
  routes/listeners remain unchanged. No browser, QUIC or TLS claim follows.
  **Evidence:** T27-T30 and T06-T11/T22/T23. Retain scrubbed commands, receipts,
  architecture/image pins and resource cleanup in `docs/evidence/p08-udp.md`.
  Record pre-existing resources and install teardown before setup. Check cleanup
  on the actual host and give every retained fixture a cleanup owner/condition.

## Checks and exit gate

Deliver one reviewable PR per task, with its necessary contract changes.
For implementation, run `go test ./...`, `go vet ./...`,
`go build ./cmd/brine`, `git diff --check` and the repository CI script.
Add race checks for reservation and recovery changes. Follow the workstation's
shared heavy-job queue when running Go checks and keep scratch/cache on `/work`.

The UDP delivery is complete only when all five tasks have acceptance evidence.
A fresh agent can deploy, inspect, update, roll back and remove the fixture
through Brine's restricted channel. Real remote UDP works on the approved
network, both runtime architectures are recorded, and failure recovery preserves
ownership. SpacetimeDB production readiness remains its own Phase 09 gate.

Start P08-02 now. Continue P08-03 and P08-04 after its contracts land, then
P08-06 and P08-08. Previews, shared ingress, TLS delivery and protocol probes
are separate deliveries and cannot hold this exit gate open.
