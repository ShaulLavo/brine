# Phase 08 UDP publication: implementation design

**Status: Approved design** for the tasks in [the UDP plan](08-udp.md), owner
requirement of 2026-11-01. This file does not repeat the plan's goals or
acceptance text. It fixes the shapes, invariants, seams and test mapping that
P08-02, P08-03, P08-04, P08-06 and P08-08 implement. The task checkboxes stay
in [08-udp.md](08-udp.md). [D10](../../docs/DECISIONS.md#d10-webtransport-starts-with-app-terminated-quic-on-approved-udp-ports)
remains the authority; nothing here widens it.

Written against `afc6f3c6`. Recheck the named files before each task.

## What Brine does and does not do

Brine publishes one approved UDP socket from an enrolled host into one app
container, beside that app's existing loopback TCP port and Caddy route. It
admits, renders, observes, recovers and releases that publication.

Brine does not terminate QUIC, proxy WebTransport, parse application frames,
mint tokens, handle certificates or hashes, or rewrite an app's discovery
document. It sends no packets to the app to decide health. It never changes
firewall, DNS, Tailscale or ACL state. Rootless Podman with pasta adds no
nftables rules, so a correct removal leaves nothing in the firewall to undo.

The first consumer is a WebTransport relay that keeps its HTTPS/WSS route
through Caddy and terminates QUIC itself on the published UDP port. That app
owns its advertised authority, certificate hash and protocol checks.

## Decisions this design makes

These choices resolve open wording in 08-udp.md. Each is the narrowest one that
meets the requirement.

1. **The operator grant pins the host port.** The app declares only its
   container port and exposure class. The per-app operator grant supplies the
   bind address, client address and host-port range. A grant of
   `min = max = 4433` is how "4433 if free and permitted" is expressed: if that
   socket is taken, the plan conflicts; Brine never silently picks another
   port the app did not advertise. A wider range lets Brine allocate the
   lowest free port, which stays stable afterwards.
2. **One mapping per app.** `[udp]` is a single table, not an array. A second
   mapping needs a new schema decision.
3. **Exact addresses only.** Grants name one IPv4 or IPv6 literal. Wildcards
   (`0.0.0.0`, `::`), IPv4-mapped IPv6, multicast and zoned addresses are
   refused. The container may bind its own wildcard inside its network
   namespace; that never widens the host publication.
4. **pasta only.** Debian 13 Podman 5 uses pasta for rootless networking.
   Readiness requires the container's observed network mode to be `pasta`.
   Host networking, slirp4netns and rootful netavark are refused.
5. **Stateless and SQLite apps only.** Managed SpacetimeDB apps refuse `[udp]`
   in this delivery (`spec.udp_managed_unsupported`). UDP never touches data
   permits, initialization, restore or archive authority.
6. **Stop keeps the reservation.** A stopped app keeps its allocation and
   reports no live listener. Only a committed removal, or a committed release
   without `[udp]`, releases it, and only after absence is proved.
7. **No environment injection.** Brine does not add `BRINE_UDP_*` variables.
   The app's own environment names its advertised authority. Plan review and
   status show the client address and port so a mismatch is visible.
8. **UDP and the HTTP range never overlap.** Policy refuses a grant range that
   overlaps `app_ports` or ports 80, 443 and 2019. TCP and UDP allocations are
   still kept in separate observations; this rule only removes the case where
   one Brine allocation could shadow the other's number.

## App definition (`internal/spec`)

```toml
[udp]
container_port = 4433
exposure = "public"   # or "tailnet"
```

```go
// internal/spec/udp.go
type UDPExposure string

const (
	UDPPublic  UDPExposure = "public"
	UDPTailnet UDPExposure = "tailnet"
)

type UDP struct {
	ContainerPort Port
	Exposure      UDPExposure
}

// App gains: UDP *UDP  // nil means no UDP publication
```

Rules, all enforced by `Parse` with parser-owned messages:

- Both keys are required; unknown keys refuse with `spec.unknown_field`.
- `container_port` uses the same bounds as the HTTP `container_port`. It may
  equal the HTTP number, since TCP and UDP are separate inside the container.
- `exposure` is exactly `public` or `tailnet` (`spec.invalid_udp`).
- `[udp]` together with `[spacetimedb]` refuses (`spec.udp_managed_unsupported`).
- No host address, host port, protocol override, network mode or Podman option
  exists in the schema. The table name is the protocol.

Omitting `[udp]` yields `App.UDP == nil`. Existing specs parse to identical
values.

## Operator policy (`internal/policy`)

```toml
[[udp_grants]]
app = "relay"
exposure = "public"
bind_address = "203.0.113.10"
client_address = "203.0.113.10"   # IP literal or DNS name; reported, never resolved
host_ports = { min = 4433, max = 4433 }
```

```go
// internal/policy/udp.go
type UDPGrant struct {
	App           string           `toml:"app" json:"app"`
	Exposure      spec.UDPExposure `toml:"exposure" json:"exposure"`
	BindAddress   string           `toml:"bind_address" json:"bind_address"`
	ClientAddress string           `toml:"client_address" json:"client_address"`
	HostPorts     PortRange        `toml:"host_ports" json:"host_ports"`
}

// document gains: UDPGrants []UDPGrant `toml:"udp_grants" json:"udp_grants,omitempty"`
func (p Policy) UDPGrant(app string) (UDPGrant, bool)
```

Policy `Parse` refuses, before any plan exists:

- more than one grant for an app;
- a bind address that is not a canonical, specified, non-multicast, unzoned
  IPv4 or IPv6 literal, or is IPv4-mapped IPv6;
- a client address that is neither an IP literal nor a canonical DNS name;
- `host_ports.min < 1024`, `min > max`, or a width above 64 ports;
- a range overlapping `app_ports` or 80, 443 or 2019;
- two grants whose ranges overlap on the same bind address.

Canonical text is `netip.Addr.String()` for addresses and lowercase without a
trailing dot for names. The whole document is already bound by `Policy.Hash()`.

`Normalize` copies the grant into desired input:

```go
// internal/policy/desired.go
type UDPDesired struct {
	ContainerPort spec.Port        `json:"container_port"`
	Exposure      spec.UDPExposure `json:"exposure"`
	Grant         UDPGrant         `json:"grant"`
}

// Desired gains: UDP *UDPDesired `json:"udp,omitempty"`
```

`Normalize` refuses `policy.udp_denied` (exit category 4, machine code
`policy_refused`) when the spec has `[udp]` and there is no grant for that app,
or the grant's exposure differs. No new machine error code is added.
`Desired.App()` reconstructs `[udp]`; `Desired.Stateless()` stays unaware of
UDP, because UDP is not data.

### Invariant: apps without UDP stay byte-identical

`UDP` is `omitempty` in desired input, plans, releases and change payloads.
An app without `[udp]` produces the same canonical desired bytes, plan payload
shape and unit file as before. Adding a grant for app A changes the policy hash,
which already appears in every app's desired input. Re-planning app B after any
policy edit is an existing update-with-no-artifact-change; it is not new UDP
behavior. P08-02 adds a test that B's rendered unit is byte-identical apart from
the plan marker line.

### Grant changes through `brine permissions`

`brine permissions plan` gains `--allow-udp-grant FILE` and
`--revoke-udp-grant APP`, following the CDN binding pattern: strict JSON bounded
to 4 KiB, reviewed before/after hashes, admin channel only. Revocation refuses
while the app holds a reservation that is not `released`. The restricted deploy
key can never edit grants.

## Inventory (`internal/target`, `internal/inventory`)

The snapshot stays schema version 1. Every new observation is required, as with
existing fields. Offline fixtures in the repository are updated in the same
change. There is no fallback for older snapshot files.

```go
// internal/target
type UDPSocket struct {
	Address string `json:"address"` // canonical literal; "0.0.0.0", "::", or "*" for ss dual-stack
	Port    Port   `json:"port"`
	App     string `json:"app,omitempty"`     // affirmative ownership proof only
	Process string `json:"process,omitempty"`
	Unit    string `json:"unit,omitempty"`
}

type UDPPublication struct {
	BindAddress   string `json:"bind_address"`
	HostPort      Port   `json:"host_port"`
	ContainerPort Port   `json:"container_port"`
	NetworkMode   string `json:"network_mode"`
}

// Snapshot gains:
//   UDPSockets     Observation[[]UDPSocket] `json:"udp_sockets"`
//   LocalAddresses Observation[[]string]    `json:"local_addresses"`
// App gains (Absent allowed):
//   UDPPublication Observation[UDPPublication] `json:"udp_publication"`
```

`UsedPorts` and `PortOwners` keep their current meaning, including the
conservative number-only union. `UDPSockets` adds the address-specific view the
UDP planner needs. `LocalAddresses` comes from a bounded read-only
`ip -j addr show` and lists canonical specified addresses. Unknown or truncated
output is unknown, never empty.

Collector seams that must change, or UDP apps become unknown:

- `inventory/listeners.go`: the existing `ss -H -lunp` pass also records
  `UDPSocket` entries. Ownership reuses `ownsSocket` and the service-cgroup
  match, but compares the exact bind address from the measured publication,
  not `127.0.0.1`.
- `inventory/runtime.go` `livePublication`: today it returns unknown when more
  than one port is published. It must accept exactly one `<n>/tcp` on
  `127.0.0.1` plus at most one `<n>/udp` on a specified address, and add
  `{{.HostConfig.NetworkMode}}` to `runtimePortFormat`.
- `inventory/listeners.go` `pinnedListenerPort`: today it requires exactly one
  `PublishPort=` line. It must parse exactly one TCP line and at most one
  `/udp` line, each with the exact grammar the renderer emits.
- `inventory/absence.go` `listenersAbsent`: retired UDP endpoints are checked by
  address and port against `UDPSockets`, with the overlap rules below.

## Ownership and overlap rules

A candidate endpoint is `(family, address, port)` with protocol UDP. An observed
UDP socket **overlaps** it when the ports match and any of these is true:

- the addresses are equal;
- the socket is `0.0.0.0` and the candidate is IPv4;
- the socket is `::` or `*`, for either family (IPV6_V6ONLY is not observable,
  so dual-stack is assumed);
- the candidate is a wildcard (refused earlier, listed for completeness).

An overlapping socket is explained only when it carries an affirmative `App`
equal to the planned app **and** that app's measured `UDPPublication` matches
the endpoint. Everything else, including empty ownership, is foreign.

A TCP socket never explains, blocks or overlaps a UDP endpoint. A UDP socket
never explains a TCP one.

## Planning (`internal/plan`)

```go
// internal/plan/udp.go
type UDPAllocation struct {
	ReservationID string           `json:"reservation_id,omitempty"` // empty only for a new allocation
	Family        string           `json:"family"`                   // "ipv4" | "ipv6"
	BindAddress   string           `json:"bind_address"`
	ClientAddress string           `json:"client_address"`
	Exposure      spec.UDPExposure `json:"exposure"`
	HostPort      target.Port      `json:"host_port"`
	ContainerPort target.Port      `json:"container_port"`
	GrantHash     string           `json:"grant_hash"` // sha256 of the canonical grant JSON
}

// A reservation is the host-side endpoint only. The container port lives in
// the release's UDPAllocation and may change without touching the reservation.
type UDPReservation struct {
	ID          string      `json:"id"`
	App         string      `json:"app"`
	State       string      `json:"state"` // staged | active | releasing
	Family      string      `json:"family"`
	BindAddress string      `json:"bind_address"`
	HostPort    target.Port `json:"host_port"`
}

const (
	ReserveUDP ChangeKind = "reserve_udp"
	ReleaseUDP ChangeKind = "release_udp"
)

const (
	UDPDenied             ConflictCode = "udp_denied"
	UDPPortOwned          ConflictCode = "udp_port_owned"
	UDPPortsExhausted     ConflictCode = "udp_ports_exhausted"
	UDPBindUnavailable    ConflictCode = "udp_bind_address_unavailable"
	UDPRuntimeUnsupported ConflictCode = "udp_runtime_unsupported"
)

// Plan gains:            UDP *UDPAllocation `json:"udp,omitempty"`
// Change gains:          UDP *UDPAllocation `json:"udp,omitempty"`
// Quadlet gains:         UDP *UDPAllocation `json:"udp,omitempty"`
// CurrentRelease gains:  UDP *UDPAllocation `json:"udp,omitempty"`
// BrineState gains:      UDPReservations []UDPReservation `json:"udp_reservations,omitempty"`
```

`Build` decides UDP after the HTTP port, in this order:

1. No `Desired.UDP` and no reservation for the app: no UDP payload at all.
2. `Desired.UDP` set: the grant's bind address must be in `LocalAddresses`
   (`udp_bind_address_unavailable`); unknown observations add `unknown_facts`
   at `udp_sockets` or `local_addresses`. The observed `passt` version must be
   known (`udp_runtime_unsupported` otherwise).
3. If the app holds a non-released reservation whose endpoint is still inside
   the current grant, retain it (`ReservationID` set, no `reserve_udp`).
4. Otherwise allocate the lowest port in the grant range with no overlapping
   foreign socket and no other app's non-released reservation on that
   address and port. None free: `udp_ports_exhausted`. A single-port grant
   whose port is taken: `udp_port_owned`. Emit `reserve_udp`.
5. A retained endpoint with an overlapping foreign socket: `udp_port_owned`.
   A changed container port alone keeps the reservation and re-renders.
6. If the app holds a reservation that the new desired input no longer covers
   (no `[udp]`, or the endpoint moved), emit `release_udp` for the old one.

`configHash` and `configurationDiff` include `UDPAllocation` (without
`ReservationID`, which is identity, not configuration). The diff field is `udp`.

Decision facts extend `portAdmissionSnapshot`: `UDPSockets` is projected to
sockets whose port lies in this app's grant range or retained allocation, on any
overlapping address. `LocalAddresses` is projected to one boolean, "grant bind
address present". Ephemeral UDP client sockets elsewhere cannot stale a plan;
a new socket in the granted range does.

Concurrent plans can compute the same free port. Only one can commit: apply
rechecks decision facts under the host mutation lock, and the store's unique
index (below) refuses a second live reservation on the same endpoint as
defense in depth.

## Persisted state (`internal/store`)

One migration, the next schema version after 33:

```sql
CREATE TABLE udp_reservations(
  id TEXT PRIMARY KEY,                        -- 'udp-' || creating operation id
  app TEXT NOT NULL,
  family TEXT NOT NULL CHECK(family IN ('ipv4','ipv6')),
  bind_address TEXT NOT NULL,
  host_port INTEGER NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
  state TEXT NOT NULL CHECK(state IN ('staged','active','releasing','released')),
  created_by TEXT NOT NULL REFERENCES operations(id),
  released_by TEXT REFERENCES operations(id),
  canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
CREATE UNIQUE INDEX udp_live_app ON udp_reservations(app) WHERE state IN ('staged','active');
CREATE UNIQUE INDEX udp_live_endpoint ON udp_reservations(bind_address,host_port) WHERE state<>'released';
CREATE TRIGGER udp_reservations_no_delete BEFORE DELETE ON udp_reservations BEGIN SELECT RAISE(ABORT,'retained reservation'); END;
CREATE TRIGGER udp_reservation_identity BEFORE UPDATE OF id,app,family,bind_address,host_port,created_by,canonical ON udp_reservations BEGIN SELECT RAISE(ABORT,'immutable reservation'); END;
```

Allowed state edges, enforced by a trigger like `legal_transition`:
`staged -> active`, `staged -> released`, `active -> releasing`,
`releasing -> released`, `releasing -> active` (a failed release rolls back to
the still-running mapping).

A container-port-only change keeps the reservation and re-renders the unit.
Moving to a different endpoint happens in one deploy: `reserve_udp` moves the
old row `active -> releasing` and inserts the new `staged` row in the same
transaction, so an app never has two `staged` or `active` rows. Commit settles
both; a failed deploy reverses both.

Releases and plans need no schema change: `UDP` lives in their existing
canonical JSON blobs. `reserveCommittedPorts` gains a sibling that loads every
non-released row into `BrineState.UDPReservations` and
`ControlInventory`, so stopped, failed and unknown apps keep their endpoints.

## Rendering (`internal/quadlet`)

`Render` takes the allocation from `plan.Quadlet.UDP` and emits, after the
existing TCP line:

```ini
PublishPort=127.0.0.1:20001:3000
PublishPort=203.0.113.10:4433:4433/udp
```

IPv6 renders as `[2001:db8::10]:4433:4433/udp`. `Render` refuses when the
allocation's grant hash, exposure or container port differ from
`Desired.UDP`, when the host port is outside the grant, or when a `reserve_udp`
change disagrees with the allocation. `validateDesired` must add the `udp`
table to its TOML reconstruction, or every UDP unit fails validation. No
`Network=`, capability, `PodmanArgs` or host socket line is added.

## Apply, lifecycle and recovery (`internal/apply`)

For an app with UDP intent, `deploymentSteps` inserts two steps; apps without
UDP keep today's list exactly:

```text
preflight, pull_image, verify_image, ensure_secrets, [prepare_data],
reserve_udp, stage_unit, quiesce_old, install_unit, reload_units, start_unit,
check_direct, check_udp, publish_route, check_routed, commit
```

A deploy that only releases UDP adds `check_udp_absent` after `check_direct`
instead of `reserve_udp` and `check_udp`.

| Step | Effect | Unknown outcome |
| --- | --- | --- |
| `reserve_udp` | One store transaction inserts the `staged` row and its journal event. Runs before the old writer stops. | None. The transaction either committed or not; resume reads the row by operation id. |
| `check_udp` | Read-only. Requires the unit file's exact `/udp` line, `podman inspect` running with network mode `pasta` and exactly the expected TCP and UDP bindings, and one owned `UNCONN` socket at the endpoint with no foreign overlap. Bounded by the existing startup deadline. | Re-run; it has no effect. |
| `check_udp_absent` | Read-only proof: no `/udp` line, no UDP binding in `podman inspect`, and no socket overlapping the old endpoint. | Re-run. |
| `commit` | Existing release commit also moves `staged -> active` and `releasing -> released` in the same transaction. | Existing commit inspection. |

Failure handling uses the existing paths:

- A failed `check_udp` is a health failure (`udp_unready`). The executor
  restores the previous unit as today. If the previous release had no UDP,
  rollback runs `check_udp_absent` and marks the staged row `released`. If
  absence cannot be proved, the operation ends `recovery_required` and the row
  stays `staged`, which keeps blocking that endpoint.
- `reconcileUnknown` compares the observed unit, container and now the UDP
  publication against the plan before resolving `install_unit`, `start_unit`
  and `stop_unit`. Unknown is inspected, never replayed.
- SIGKILL or reboot between steps: resume reads the journal and the
  reservation row. A `staged` row with no committed release is resolved by
  inspection: if the new unit is installed and `check_udp` passes, continue;
  otherwise follow the failure path above.

Lifecycle plans (`internal/apply/lifecycle.go`):

- `stop_app`: the reservation stays `active`. Status shows the endpoint as
  reserved with no expected listener. No UDP step.
- `start_app` and `restart_app`: add `check_udp` after `check_direct`.
- Rollback (`internal/apps` rollback service): the normal planner runs on the
  selected release's desired input against current policy. The retained
  reservation is reused when the grant still covers it; a revoked grant makes
  the rollback plan conflict with `udp_denied`. Rollback to a release without
  UDP emits `release_udp`. Stateless compatibility is unchanged because
  `Stateless()` ignores UDP.
- `remove_app` (`internal/apply/remove.go`): after the unit and route are gone,
  `check_udp_absent` must pass before the retirement commit moves the row to
  `released`. Failure ends `recovery_required` with the row `releasing`.
  Persistent removal still goes through the archive path; UDP grants no data
  authority.

Terminal recovery resolution (`brine resolve`) treats a `staged` or `releasing`
row as part of the operation's owned state: resolution may only settle it with
the same read-only proofs.

## Status, diagnosis and output

`brine status` app records gain a nullable `udp` object; release records gain
a nullable `udp` allocation. The human view shows one line per published app.

```json
"udp": {
  "protocol": "udp",
  "exposure": "public",
  "bind_address": "203.0.113.10",
  "client_address": "203.0.113.10",
  "host_port": 4433,
  "container_port": 4433,
  "reservation": "active",
  "local": "ready",
  "local_reasons": [],
  "remote": "unverified"
}
```

- `reservation`: `staged`, `active`, `releasing`.
- `local`: `ready`, `not_ready`, `stopped` or `unknown`. `ready` needs the same
  facts as `check_udp`. HTTP health stays in `health.direct`; overall app
  readiness requires both.
- `local_reasons`: closed set `mapping_missing`, `mapping_mismatch`,
  `network_mode_unsupported`, `socket_missing`, `socket_foreign`,
  `bind_address_absent`, `observation_unknown`.
- `remote` is always `unverified`. Brine has no client vantage. External probe
  results belong to evidence files, with their vantage and timestamp.

Drift adds the field `udp` when the measured publication differs from the
committed allocation. Diagnosis (`internal/diagnose/rules.go`) adds rules that
map each `local_reasons` value to a safe next step, plus `udp_remnant` for an
unexplained socket on a released or retired endpoint, and an informational
`udp_operator_network` reminder that host and provider firewalls are the
operator's job. Plan presentation (`internal/planview`) shows the allocation,
`reserve_udp` and `release_udp`. `docs/reference/commands.md` is regenerated
for the new `permissions` flags.

## Task boundaries and file ownership

Run the tasks in this order. Each is one reviewable PR with its contract text.

| Task | Owns | Must not touch |
| --- | --- | --- |
| P08-02 | `internal/spec/udp.go`, spec parse wiring; `internal/policy/udp.go`, `policy.go`, `desired.go`; `internal/target` UDP types and validation; `inventory/listeners.go` `UDPSockets`, new local-address probe; `internal/plan/udp.go`, `plan.go`, `decision_facts.go`, `diff.go`; store migration and `udp_reservations.go`; `internal/planview`; `permissions` UDP grant flags; offline fixtures; CONTRACTS UDP section | Quadlet rendering, apply steps |
| P08-03 | `internal/quadlet/render.go` and `verify_linux.go`; `inventory/runtime.go` `livePublication`; `inventory/listeners.go` `pinnedListenerPort`; rootless runtime test under `integration/` | Readiness output, apply |
| P08-04 | `inventory/listeners.go` UDP ownership proof and per-app `UDPPublication`; `internal/apps` status and drift; `internal/diagnose`; the shared `UDPReady` check used by status and apply | Store schema, apply sequencing |
| P08-06 | `internal/apply` steps, `deploymentSteps`, lifecycle, remove, reconcile, recovery; store state transitions in commit and retirement; `internal/apps` rollback; `internal/host` wiring | Spec and policy shapes |
| P08-08 | `fixtures/udp-echo/` image source; physical harness under `integration/`; `.internal/evidence/p08-udp.md` | Production packages, except fixes for defects found |

P08-03 and P08-04 both edit `inventory/listeners.go`. Land P08-03 first;
P08-04 may build its status and diagnosis output in parallel and rebase.

## Acceptance to test mapping

| Requirement | Test | Where |
| --- | --- | --- |
| No UDP intent leaves apps unchanged | Golden: existing spec, desired bytes, plan and unit are byte-identical; B unchanged after A's grant | `spec`, `policy`, `plan`, `quadlet` tests |
| Deny by default | Spec with `[udp]` and no grant, wrong exposure, grant for another app: `policy.udp_denied`, no plan saved | `policy`, `apps` service tests |
| Unknown fields, bad ports, wildcards | Parser and policy tables, including `0.0.0.0`, `::`, `::ffff:a.b.c.d`, port 80, range overlapping `app_ports` | `spec/udp_test.go`, `policy/udp_test.go` |
| Foreign ownership refuses before effects | Planner tables: equal address, IPv4 wildcard, `::`, `*`, empty owner, other app's reservation; TCP on same number does not conflict | `plan/udp_test.go` (T28) |
| Unknown observation refuses | `udp_sockets`, `local_addresses`, publication unknown give `unknown_facts` | `plan/udp_test.go` |
| Concurrent plans cannot share an endpoint | Two plans, one port: second apply stale under the lock; direct store insert hits the unique index | `store/udp_reservations_test.go`, `apply` race test (T26) |
| Exact allocation and rendering | Render goldens for IPv4 and IPv6; refusals for grant-hash, range and container-port mismatch | `quadlet/render_test.go` (T27) |
| Generated equals actual mapping | Real rootless Podman on arm64 and amd64: inspect bindings and network mode equal render | `integration/` rootless test (T27) |
| HTTP healthy plus missing or foreign UDP is not ready | Collector fakes: missing socket, foreign cgroup, extra binding, `slirp4netns` mode | `inventory`, `apps` status goldens (T29) |
| Owned socket does not imply remote reachability | `remote` stays `unverified` in every status golden | `apps` goldens (T29) |
| Restart, stop and start keep ownership | Lifecycle executor tests; stopped status shows reservation without listener | `apply/lifecycle_test.go` |
| Rollback restores mapping and reservation | Failed `check_udp` restores previous; rollback to no-UDP release proves absence then releases | `apply` tests (T30) |
| Removal leaves no socket or claim | Absence proof required; remnant gives `recovery_required` and keeps the row | `apply/remove_test.go`, `inventory/absence_test.go` (T30) |
| Interruption at each new boundary | Kill after `reserve_udp`, during `check_udp`, before and after commit, during release; resume inspects | `apply/recovery_test.go`, real subprocess kills (T30) |
| Real UDP through the managed publication | Physical drill below | P08-08 evidence |

## Physical evidence (P08-08)

The fixture is a small non-root image with an HTTP health endpoint and a bounded
UDP echo that replies with the request nonce plus its release ID. It also
serves its own receive and reply counters on the HTTP side; those are the
task-owned counters. The echo is test evidence, not a Brine readiness check.

On the D3 Pi (arm64), with a tailnet grant bound to the Pi's tailnet address:

1. Record pre-existing units, containers, images, policy bytes and UDP sockets.
   Back up the policy file. Install teardown first: remove fixture apps,
   revoke their grants, restore policy bytes, remove fixture images by digest.
2. Deploy two fixture apps with distinct allocations. A separate client on the
   tailnet sends nonces through the allocated ports with a bounded number of
   read-only attempts and records its vantage and timestamps.
3. Update, failed health, rollback, SSH disconnect during apply, SIGKILL at
   each new boundary, reboot, then removal. After each, record status, the
   reservation row and `ss -lunp` for the endpoint.
4. A controlled unreachable client (a port with no grant) must time out
   without any host network change.
5. Run teardown and check on the host that no fixture unit, container, image,
   grant or socket remains. Record anything retained with size and reason.

The amd64 runtime result needs a host the owner authorizes; see below.

Measure, do not assume: whether pasta preserves client source addresses for
UDP, its UDP flow idle timeout against the app's keepalive, and whether the
unit restarts cleanly when the bind address appears after the user manager
starts at boot (a tailnet address can arrive late).

## Owner decisions and blockers

Nothing in this design changes a firewall, DNS record, Tailscale setting,
operator policy or production host. The requirement alone does not authorize
those.

- **Accept the decisions above**, chiefly grant-pinned host ports with no
  app-chosen port, one mapping per app, pasta only, and no managed-engine UDP.
- **amd64 runtime evidence** (blocks P08-08's two-architecture gate): name a
  disposable amd64 host, or authorize a bounded fixture-only drill on the VPS
  with explicit teardown.
- **VPS grant** (blocks the relay's public UDP): an operator
  `brine permissions plan --allow-udp-grant` for the relay app with the public
  bind address and port 4433. This is policy widening and needs its own
  approval. The VPS's enrollment state must be checked first, not assumed.
- **Inbound UDP on the VPS** (blocks remote reachability): the host and any
  provider firewall must already admit UDP 4433. Brine will not open it.
- **Relay bind versus advertised authority** (blocks WebTransport acceptance,
  not Brine tasks): the relay currently derives its advertised URL from its
  numeric bind address. Inside the container it binds its own interface, so the
  relay lane must separate bind and advertised authority before public
  WebTransport is enabled.

P08-02 through P08-06 have no external blocker; their unit and fake-host tests
run without any host. P08-03's real rootless test and P08-08 run on the Pi under
D3.
