# Phase 08 UDP publication: implementation design

**Status: Approved design** for the tasks in [the UDP plan](08-udp.md), owner
requirement of 2026-11-01. This file does not repeat the plan's goals or
acceptance text. It fixes the shapes, invariants, seams and test mapping that
P08-02, P08-03, P08-04, P08-06 and P08-08 implement. The task checkboxes stay
in [08-udp.md](08-udp.md). [D10](../../docs/DECISIONS.md#d10-webtransport-starts-with-app-terminated-quic-on-approved-udp-ports)
remains the authority; nothing here widens it.

Written against `afc6f3c6`. The incarnation, reservation transition, proof,
resume and rollback sections were revised against `876c7b13`. Recheck the
named files before each task.

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
   whose plan retires that endpoint, releases it, and only after that exact
   endpoint's absence is proved.
7. **No environment injection.** Brine does not add `BRINE_UDP_*` variables.
   The app's own environment names its advertised authority. Plan review and
   status show the client address and port so a mismatch is visible.
8. **UDP and the HTTP range never overlap.** Policy refuses a grant range that
   overlaps `app_ports` or ports 80, 443 and 2019. TCP and UDP allocations are
   still kept in separate observations; this rule only removes the case where
   one Brine allocation could shadow the other's number.
9. **An app incarnation is its founding plan.** Each live release head records
   the hash of the plan whose release created it. Removal ends it; reusing the
   name founds a new one. UDP claims, plans and ownership proofs bind to it.
   Stateless apps get it without any data authority.

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

- `inventory/listeners.go`: change the UDP pass to `ss -H -lunpe` and record
  `UDPSocket` entries. The extended output must contain `ino:` for ownership.
  Keep the existing bounded parser, stable PID/start-time, cgroup and
  descriptor-to-inode checks in `ownsSocket`; missing inode stays unknown.
  Compare the exact bind address from the measured publication, not
  `127.0.0.1`. Update fake command keys and absence probes, and test captured
  output from this actual command on the supported rootless runtime.
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

An overlapping socket is explained only when all of these hold:

- it carries an affirmative `App` equal to the planned app;
- that app's measured `UDPPublication` matches the endpoint;
- the app's observed unit hashes equal its current release's units, whose
  `# Brine-owned plan=` marker names the plan that binds the head's
  incarnation (during apply, `check_udp` uses this operation's rendered unit);
- the app holds the `active` row for that endpoint in that incarnation.

With no live head nothing is explained, so a recreated name never inherits a
socket from an earlier incarnation. Everything else, including empty
ownership, is foreign.

A TCP socket never explains ownership of a UDP endpoint or occupies a UDP
candidate. Preserve the existing conservative HTTP admission rule: `UsedPorts`
remains a TCP/UDP numeric union, so a UDP socket can still block a new HTTP
candidate. Keep the existing treatment of retained TCP ports outside a revised
range. UDP ownership never explains a TCP listener. This delivery does not
replace HTTP allocation with a new protocol-specific algorithm.

## App incarnation

Phase 08 requires UDP ownership to bind an immutable app incarnation, and an
app name is not one: `RetireApp` deletes the release head but keeps history,
and `target.App.RuntimeAbsent` lets a later deploy reuse the name.
`BrineState.DataIncarnations` covers persistent data only, and a stateless
app must not need data authority to get an identity.

The incarnation ID is the hash of the plan whose committed release created the
live release head.

| Event | Incarnation |
| --- | --- |
| First committed release of a name (a `Create` plan) | Founded: the head insert sets `release_heads.incarnation_id = release.PlanID` |
| Later release, rollback or replica revision | Unchanged: the head upsert never updates the column |
| `RetireApp` | Ended with the deleted head. Retained releases keep their plans |
| Name reused after removal | A new `Create` plan at a later generation founds a new ID |
| `Create` that fails or rolls back | Founds nothing. Its claim is reverted before `Failed` or `RolledBack`; a `recovery_required` end keeps it for resolution (below) |

The plan hash is the right key because it is fixed before any effect. It is
also identical for a deploy and every `resolve` successor, since
`resolutionFamilyAvailable` requires one `PlanID` across the family. A release
ID would not work: a resolution commits under the successor's ID. The hash is
unique per head because it binds the observed generation, and every commit or
removal advances that generation. One `Create` plan can therefore found at most
one head.

The migration rebuilds `release_heads` with `incarnation_id TEXT NOT NULL`,
filled in from each existing head's current release `plan_id`. No existing
release has UDP, so nothing refers to an older value. Release blobs, HTTP plans
and HTTP decision inputs do not change. The column reaches the planner only
through `BrineState.UDP`, which `Build` removes for apps with no UDP
involvement.

## Planning (`internal/plan`)

```go
// internal/plan/udp.go
type AppIncarnation struct {
	App string `json:"app"`
	ID  string `json:"id"` // release_heads.incarnation_id
}

type UDPEndpoint struct {
	Family      string      `json:"family"` // "ipv4" | "ipv6", derived from BindAddress
	BindAddress string      `json:"bind_address"`
	HostPort    target.Port `json:"host_port"`
}

// UDPAllocation is configuration: what one release publishes.
type UDPAllocation struct {
	Endpoint      UDPEndpoint      `json:"endpoint"`
	ContainerPort target.Port      `json:"container_port"`
	ClientAddress string           `json:"client_address"`
	Exposure      spec.UDPExposure `json:"exposure"`
	GrantHash     string           `json:"grant_hash"` // sha256 of the canonical grant JSON
}

// UDPReservation is one durable row that is not released.
type UDPReservation struct {
	ID          string      `json:"id"` // "udp-" + claiming operation ID
	App         string      `json:"app"`
	Incarnation string      `json:"incarnation"`
	State       string      `json:"state"`           // staged | active | releasing
	Owner       string      `json:"owner,omitempty"` // operation holding a staged or releasing row
	Endpoint    UDPEndpoint `json:"endpoint"`
}

type UDPState struct {
	Incarnations []AppIncarnation `json:"incarnations"` // every live head
	Reservations []UDPReservation `json:"reservations"` // every row not released
}

// UDPPlan is the plan's whole UDP intent and reservation transition.
// A claim (a new staged row on Next's endpoint) is implied by Next != nil && Keep == "".
type UDPPlan struct {
	Incarnation string          `json:"incarnation,omitempty"` // empty only in a Create plan: this plan founds it
	Exact       *UDPEndpoint    `json:"exact,omitempty"`       // rollback's selected endpoint
	Next        *UDPAllocation  `json:"next,omitempty"`        // nil: no UDP after this plan
	Keep        string          `json:"keep,omitempty"`        // active row kept unchanged
	Retire      *UDPReservation `json:"retire,omitempty"`      // active row this plan releases
}

// ResolvedIncarnation returns p.Hash for a Create plan, else p.UDP.Incarnation.
func (p Plan) ResolvedIncarnation() string

const (
	ReserveUDP ChangeKind = "reserve_udp" // presentation of a claim
	ReleaseUDP ChangeKind = "release_udp" // presentation of a retire
)

const (
	UDPDenied             ConflictCode = "udp_denied"
	UDPPortOwned          ConflictCode = "udp_port_owned"
	UDPPortsExhausted     ConflictCode = "udp_ports_exhausted"
	UDPBindUnavailable    ConflictCode = "udp_bind_address_unavailable"
	UDPRuntimeUnsupported ConflictCode = "udp_runtime_unsupported"
	UDPUnsettled          ConflictCode = "udp_reservation_unsettled"
)

// Plan gains:            UDP *UDPPlan          `json:"udp,omitempty"`
// Change gains:          UDP *UDPEndpoint      `json:"udp,omitempty"`
// Quadlet gains:         UDP *UDPAllocation    `json:"udp,omitempty"` // always Plan.UDP.Next
// CurrentRelease gains:  UDP *UDPAllocation    `json:"udp,omitempty"` // its own plan's UDP.Next
// Removal gains:         UDP *UDPReservation   `json:"udp,omitempty"` // active row the removal releases
// BrineState gains:      UDP *UDPState         `json:"udp,omitempty"`
// Input gains:           UDPExact *UDPEndpoint // rollback, and rebuilds of a plan with Exact
```

`LoadBrineState` fills `UDP` on every load and fills `CurrentRelease.UDP` from
the release's stored plan, so releases need no schema change. Execution reads
`Plan.UDP`; the two change kinds exist only for plan review.

The transition is a property of `Plan.UDP`:

| Transition | `Retire` | `Keep` | `Next` | Claim |
| --- | --- | --- | --- | --- |
| none (`Plan.UDP == nil`) | | | | |
| create | | | B | B |
| retain (container port may change) | | A | A | |
| move | A | | B | B |
| release only | A | | nil | |

`Build` decides UDP after the HTTP port, in this order:

1. **Projection.** The app is UDP-involved when `Desired.UDP`, its current
   release's `UDP`, `UDPExact` or any of its rows is set. If not, `Build` sets
   `in.State.UDP = nil` before `canonicalState`, emits no UDP payload and
   leaves the decision input byte-identical. Otherwise it keeps this app's
   incarnation, this app's rows and other apps' rows that overlap a candidate
   endpoint. The candidates are the grant range on its bind address, the app's
   active endpoint and `UDPExact`.
2. **Unsettled rows.** Any `staged` or `releasing` row of this app gives
   `udp_reservation_unsettled` at field `udp.reservation`. Only the row's owner
   family settles it, through its own resume or `brine resolve`. A later plan
   never adopts, releases or ignores it, and this includes plans with no
   `[udp]`.
3. **Incarnation.** `Incarnation` is the head's ID when a current release
   exists, and empty for `Create`. An active row in another incarnation, an
   active row with no head, or a disagreement between the active row and
   `CurrentRelease.UDP` is `artifact_drift` at `udp.reservation`.
4. **No `Desired.UDP`.** An active row A becomes `Retire`. With no row, there
   is no UDP payload.
5. **`Desired.UDP`.** The grant's bind address must be in `LocalAddresses`
   (`udp_bind_address_unavailable`). Unknown `udp_sockets` or
   `local_addresses` give `unknown_facts`, and the observed `passt` version must
   be known (`udp_runtime_unsupported`). Choose the endpoint:
   - if `UDPExact` is set, use it unchanged. It must equal the grant's bind
     address and lie in its range (`udp_denied`).
   - otherwise keep the active endpoint A while the grant still covers it;
   - otherwise take the lowest port in the range with no overlapping socket and
     no other app's row (`udp_ports_exhausted`; `udp_port_owned` for a taken
     single-port grant).
   An unexplained overlapping socket or another app's row on the chosen
   endpoint gives `udp_port_owned`. If the endpoint equals A, `Keep = A.ID`.
   Otherwise the plan claims it and retires A when A exists.
6. **Changes.** Emit `reserve_udp` for a claim and `release_udp` for a
   retire. A plan is `NoOp` only when the transition is none or retain and the
   configuration hash is equal.

`configHash` and `configurationDiff` include `Next`. They leave out
`Incarnation` and row IDs, which are identity, not configuration. The diff
field is `udp`. Decision facts extend `portAdmissionSnapshot`: `UDPSockets` is
projected to sockets that overlap a candidate endpoint, and `LocalAddresses`
to one boolean, "grant bind address present". Add these as optional pointer
fields in the existing explicit `decisionFacts` projection, not unconditional
fields copied from the raw snapshot. When the app has no UDP involvement,
omit them entirely, along with `BrineState.UDP`; zero or unknown observations
would still change the old bytes. Ephemeral UDP client sockets elsewhere
cannot stale a plan; a new socket in the granted range does.

Concurrent plans can compute the same free port. Only one can commit: apply
rechecks decision facts under the host mutation lock, and the store's unique
index (below) refuses a second live reservation on the same endpoint as
defense in depth.

## Persisted state (`internal/store`)

One migration, the next schema version after 33. It also adds the
`release_heads.incarnation_id` column described above.

```sql
CREATE TABLE udp_reservations(
  id TEXT PRIMARY KEY,                     -- 'udp-' || claiming operation id
  app TEXT NOT NULL,
  incarnation TEXT NOT NULL,               -- plan's resolved incarnation
  family TEXT NOT NULL CHECK(family IN ('ipv4','ipv6')),
  bind_address TEXT NOT NULL,
  host_port INTEGER NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
  state TEXT NOT NULL CHECK(state IN ('staged','active','releasing','released')),
  owner TEXT REFERENCES operations(id),    -- holder of a staged or releasing row
  created_by TEXT NOT NULL REFERENCES operations(id),
  released_by TEXT REFERENCES operations(id),
  canonical BLOB NOT NULL CHECK(length(canonical)<=4096),
  CHECK((state IN ('staged','releasing')) = (owner IS NOT NULL)),
  CHECK((state = 'released') = (released_by IS NOT NULL)));
CREATE UNIQUE INDEX udp_live_endpoint ON udp_reservations(bind_address,host_port) WHERE state<>'released';
CREATE UNIQUE INDEX udp_one_claim ON udp_reservations(app) WHERE state='staged';
CREATE UNIQUE INDEX udp_one_holder ON udp_reservations(app) WHERE state IN ('active','releasing');
CREATE TRIGGER udp_reservations_no_delete BEFORE DELETE ON udp_reservations BEGIN SELECT RAISE(ABORT,'retained reservation'); END;
CREATE TRIGGER udp_reservation_identity BEFORE UPDATE OF id,app,incarnation,family,bind_address,host_port,created_by,canonical ON udp_reservations BEGIN SELECT RAISE(ABORT,'immutable reservation'); END;
```

A `legal_transition`-style trigger allows only `staged -> active`,
`staged -> released`, `active -> releasing`, `releasing -> released` and
`releasing -> active`. A released row never returns: a later claim on the same
endpoint inserts a new row. Rows do not count toward `Store.Generation`, so
preparing a reservation never makes the operation's own plan stale.

The **owner family** of an operation is the operation itself plus its
`recovery_of` ancestors with the same plan ID, the chain that
`resolutionFamilyAvailable` already walks. Store methods change a `staged` or
`releasing` row only when its `owner` is in the acting operation's family.

| Method | Step | One transaction | Repeat and read-back |
| --- | --- | --- | --- |
| `PrepareUDP(op, p)` | `prepare_udp` | Retire row `active -> releasing`, owner `op`; then insert `udp-<op>` as `staged`, owner `op`, incarnation `p.ResolvedIncarnation()` | Rows already prepared by the family: success, no write. Any other shape: `ErrConflict` |
| `UDPPreparation(op, p)` | reconcile, recovery | Read only: `unprepared`, `prepared`, `committed`, `reverted` or `unresolved` | |
| `RevertUDP(op, p)` | `revert_udp`, `rollback_udp` | Claim `staged -> released`, `released_by = op`; then Retire `releasing -> active`, owner cleared | Already reverted: success, no write |
| `CommitRelease` (extended) | `commit` | Existing release insert and head upsert; Retire `releasing -> released`; then claim `staged -> active`; assert the Keep row is still active; assert every touched row's incarnation equals `incarnation_id` | Existing read-back, plus the rows' committed shape |
| `RetireApp` (extended) | `retire_app` | Existing head delete; `Removal.UDP` row `releasing -> released`. Refuses if the app holds any other row that is not released | Existing `AppRetired`, plus the row |

`udp_one_holder` covers `active` and `releasing`, so a move commit must release
the old row before activating the claim; the statement order above does that.
A revert may run in either order; it releases the claim first.

`reserveCommittedPorts` gains a sibling that loads every row that is not
released into `ControlInventory` for status. Planning reads rows only through
`BrineState.UDP`; they are not snapshot facts.

## Rendering (`internal/quadlet`)

`Render` takes the allocation from `plan.Quadlet.UDP` and emits, after the
existing TCP line:

```ini
PublishPort=127.0.0.1:20001:3000
PublishPort=203.0.113.10:4433:4433/udp
```

IPv6 renders as `[2001:db8::10]:4433:4433/udp`. `Render` refuses when the
allocation's grant hash, exposure or container port differ from
`Desired.UDP`, when the host port is outside the grant, or when
`Quadlet.UDP` differs from `Plan.UDP.Next`. `validateDesired` reconstructs the
`udp` table and explicitly checks the reconstructed UDP intent against the
allocation. Its existing name/image comparison alone does not reject omitted
UDP, so it cannot serve as the execution guard during partial implementation.
No `Network=`, capability, `PodmanArgs` or host socket line is added.

## Apply, lifecycle and recovery (`internal/apply`)

`deploymentSteps(p plan.Plan, d policy.Desired)` derives the step list from the
plan, not from desired input alone. With `Plan.UDP == nil` the list is exactly
today's:

```text
preflight, pull_image, verify_image, ensure_secrets, [prepare_data],
[prepare_udp], stage_unit, quiesce_old, install_unit, reload_units, start_unit,
check_direct, [check_udp], [check_udp_retired], publish_route, check_routed, commit
```

- `prepare_udp` runs when the plan claims or retires: `PrepareUDP`, before the
  old writer stops.
- `check_udp` runs when `Next` is set.
- `check_udp_retired` runs when `Retire` is set.

The recovery prefix and the resolution state map gain the same names:
`prepare_udp` is `Preparing`, and both checks are `Checking`.

### Endpoint proofs

Both proofs are read-only, bounded by the existing startup deadline, shared
with status, and safe to re-run. An unknown observation makes either proof
unknown, never true. Neither proof removes, stops or edits anything it finds.

- **`UDPReady(app, allocation, unitHash)`**: the installed unit has hash
  `unitHash` and exactly one `/udp` line equal to the allocation. `podman
  inspect` shows a running container in network mode `pasta` with exactly the
  expected TCP binding plus this UDP binding. Exactly one owned `UNCONN`
  socket sits at the exact endpoint, with no other overlapping socket.
- **`UDPAbsent(app, endpoint)`**: the app's installed unit, if any, has no
  `/udp` line for the endpoint; the app's container, if any, has no binding
  for it; and no observed UDP socket overlaps it, whoever owns it. The proof
  names one endpoint, so the app's legitimate publication on another endpoint
  does not affect it. A move always changes the endpoint, and exact grant
  addresses rule out a wildcard replacement, so the replacement socket never
  overlaps the retired endpoint.

### Transitions and proofs

| Transition | Before commit | Commit transaction | Compensation proof after the old unit is restored | Compensation transaction |
| --- | --- | --- | --- | --- |
| create B | `UDPReady(B)` | claim `staged -> active`; head founded with `incarnation_id = p.Hash` | `UDPAbsent(B)` after `rollback_unit` | `RevertUDP`: claim released |
| retain A | `UDPReady(A, next)` | Keep row asserted active | `UDPReady(A, previous)` | none |
| move A to B | `UDPReady(B)` and `UDPAbsent(A)` | A `releasing -> released`, then B `staged -> active` | `UDPReady(A, previous)` and `UDPAbsent(B)` | `RevertUDP`: B released, then A active |
| release A | `UDPAbsent(A)` | A `releasing -> released` | `UDPReady(A, previous)` | `RevertUDP`: A active |
| remove app, A | `UDPAbsent(A)` after unit removal | `RetireApp`: head deleted, A released | none: removal never compensates | |

A healthy B with a remnant at A fails `check_udp_retired`. The failure is
`udp_unready`, which enters rollback. A restored HTTP service with A missing,
or a B still present after restoration, fails the compensation proof and ends
`recovery_required` with both rows unchanged.

### Failure handling

The executor's existing paths gain these UDP steps:

1. **Nothing quiesced or installed.** This covers a failure in `stage_unit`,
   a `prepare_udp` read back as not applied, or a stale resume. If the family
   journaled a `prepare_udp` intent, `x.fail` runs `revert_udp` (`RevertUDP`,
   state `Preparing`, so the existing `Preparing -> Failed` edge still
   applies) before the `Failed` terminal. No runtime
   proof is needed. The journal shows no `quiesce_old` or `install_unit`
   intent, so the old publication was never touched and no unit of this
   operation exists. An unknown result is read back with `UDPPreparation`, and
   an unresolved read ends `recovery_required`.
2. **Quiesced or installed.** After the existing rollback steps, rollback adds
   `rollback_udp_check`, the compensation proof from the table, then
   `rollback_udp` (`RevertUDP`; skipped for retain), then `RolledBack`. A
   failed or unknown proof ends `recovery_required` and leaves the rows alone.
   Rollback paths created during resolution gain the same two names.
3. **Codes that already end `recovery_required`** (`journal_failed`,
   `reload_unknown`, `interrupted`, `rollback_failed`) still do. Their rows
   keep their owner. Only that family's resume or `brine resolve` settles
   them, using the same proofs.

As a result, `Failed`, `RolledBack` and `Succeeded` are never reached while
this family holds a `staged` or `releasing` row. Tests assert this after every
failure injection.

### Operation-bound resume

After `prepare_udp`, the store holds this operation's own expected effects: a
staged claim, and possibly a releasing retire. A plain rebuild would see
those effects as drift. Every fresh-plan comparison of a UDP plan therefore
first undoes exactly this family's preparation and nothing else:

```go
// internal/plan/udp_resume.go

// UnapplyUDP returns s as it was before family's prepare_udp for p: it drops
// the one staged claim on p.UDP.Next owned by family with p's resolved
// incarnation, and returns p.UDP.Retire from releasing to active. prepared
// reports whether that shape was found. Any other family-owned row, or an
// expected row with another endpoint, incarnation or state, is an error and
// the plan cannot resume. Rows of other families pass through unchanged.
func UnapplyUDP(s BrineState, p Plan, family []string) (out BrineState, prepared bool, err error)

// RebuildInput sets in.UDPExact = p.UDP.Exact and applies UnapplyUDP.
func RebuildInput(in Input, p Plan, family []string) (Input, bool, error)
```

The three existing comparisons call `RebuildInput` before building: host
`Executor.Run` (family `[op]`), `apply.Executor.run` preflight (the op plus its
resolution owners) and `inspectRecovery`. The original plan, its allocation
and its hash stay unchanged. Reservation state is never dropped from hashes;
another family's claim, or a new socket in the granted range, still makes the
rebuilt plan differ.

`inspectRecovery` changes in two places:

- A `prepare_udp` boundary without a completed outcome is read back.
  `prepared` marks the step completed. `unprepared` leaves it uncompleted, so
  `ResumeForward` runs the single idempotent transaction again; this repeats
  no unknown effect. `unresolved` requires recovery.
- When the rebuild differs and neither `quiesced` nor `installed` is set, a
  prepared family now chooses `RestorePrevious` instead of `RequireRecovery`.
  `Recover` then reaches `x.fail`, which reverts the reservation and ends
  `Failed`.

| Kill point | Read-back | Decision |
| --- | --- | --- |
| `prepare_udp` intent, transaction not committed | `unprepared` | `ResumeForward` if the rebuild is equal; `prepare_udp` runs once |
| `prepare_udp` intent, transaction committed | `prepared` | Mark completed; `ResumeForward` if the rebuild is equal |
| `prepare_udp` completed, before `stage_unit` | `prepared` | `UnapplyUDP`, equal rebuild, `ResumeForward`; no second insert |
| Before `quiesce_old` with a foreign claim or socket since planning | `prepared` | `RestorePrevious`, `revert_udp`, `Failed` |
| After `quiesce_old` or `install_unit` | any | Existing `RestorePrevious`, plus the compensation proof and revert |
| `commit` intent or unknown | `committed` | Existing commit read-back, plus the rows' committed shape: `FinishSucceeded` |

### Lifecycle, rollback and removal

- `stop_app` keeps the row `active`. Status shows the endpoint as reserved
  with no expected listener. There is no UDP step.
- `start_app` and `restart_app` add `check_udp` against the current release's
  allocation after `check_direct`. `BuildLifecycle` refuses with
  `udp_reservation_unsettled` while the app has an unsettled row.
- **Rollback** (`apps.Service.Rollback`). When the selected release's stored
  plan has `UDP.Next`, the current release has UDP, or the app has a row, the
  stateless path does three things:
  1. It takes `selected := stored.UDP.Next.Endpoint` (nil when the selected
     release had no UDP).
  2. It renormalizes the selected desired input under the current policy:
     `policy.Normalize(desired.App(), pol)`, as `managed_rollback.go` already
     does. The current grant replaces the historical one. A revoked grant or a
     changed exposure refuses `policy.udp_denied`. The container port and HTTP
     fields stay as selected.
  3. It builds with `UDPExact: selected`.

  Step 5 above then restores that exact endpoint or conflicts; it never
  reallocates. HTTP-only rollbacks keep today's path unchanged.

  | Case | Plan |
  | --- | --- |
  | Selected R1 used A, current uses B, grant covers both | move: claim A, retire B |
  | A has a foreign socket or another app's row | `udp_port_owned` |
  | A outside the current grant | `udp_denied` |
  | A's historical row released | claim A as a new row; never `released -> active` |
  | Selected release from an earlier incarnation | claim A under the current incarnation; old rows untouched |
  | Selected release had no UDP | release only: retire the current row |

- **Removal** (`internal/apply/remove.go`). `BuildRemove` puts the active row
  in `Removal.UDP`; an unsettled row conflicts. The steps become `preflight,
  [prepare_udp], withdraw_route, stop_unit, remove_unit, reload_units,
  [check_udp_retired], retire_app`. Every failure keeps removal's existing
  `recovery_required` result. A prepared row stays `releasing` under its
  owner. Removal recovery reads the row against `Removal.UDP`: `active` means
  `prepare_udp` was not applied, `releasing` under the family means it was,
  and `released` together with `AppRetired` means retirement committed.
  Persistent removal still goes through the archive path; UDP grants no data
  authority.

Terminal recovery resolution (`brine resolve`) needs no new mechanism. The
successor's family includes the source, so its continuation or compensation
settles the source's rows with the same proofs, and with nothing else.

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

- `reservation`: `staged`, `active` or `releasing`. An unsettled row also
  reports its owner operation, the one `brine resolve` acts on.
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

### Fail closed until the complete lifecycle lands

P08-02 through P08-04 stage contracts and observations, not executable UDP.
P08-02 adds a conflict-only `udp_execution_unavailable` result whenever UDP
intent or a UDP reservation transition is involved. It also adds explicit
consumer refusals in host/apply validation before any mutation. A handcrafted
plan with a UDP desired input must not bypass that refusal. The pre-P08-03
renderer rejects UDP intent rather than silently rendering HTTP alone.

P08-03 may render and verify UDP in isolated renderer/runtime tests, but does
not remove the planner or host/apply refusals. P08-04 adds shared observations
and readiness without changing that activation boundary. Existing HTTP-only
apps keep their normal paths and unchanged bytes.

P08-06 removes the temporary conflict and all execution refusals atomically
with the complete reservation, readiness, compensation, lifecycle, removal and
recovery integration. These are guards for an incomplete same-version feature,
not old/new binary compatibility branches or runtime feature flags. No earlier
merge may commit UDP intent as an HTTP-only release or publish an unreserved
UDP endpoint. Tests exercise the direct host/apply consumers as well as CLI
planning at each intermediate merge boundary.

| Task | Owns | Must not touch |
| --- | --- | --- |
| P08-02 | `internal/spec/udp.go`, spec parse wiring; `internal/policy/udp.go`, `policy.go`, `desired.go`; `internal/target` UDP types and validation; `inventory/listeners.go` `UDPSockets`, new local-address probe; `internal/plan/udp.go`, `plan.go`, `decision_facts.go`, `diff.go`; store migration and `udp_reservations.go`; `internal/planview`; `permissions` UDP grant flags; offline fixtures; CONTRACTS UDP section; narrow renderer/host/apply refusals that keep UDP conflict-only | UDP publication and effect sequencing |
| P08-03 | `internal/quadlet/render.go` and `verify_linux.go`; `inventory/runtime.go` `livePublication`; `inventory/listeners.go` `pinnedListenerPort`; isolated rootless runtime test under `integration/` | Readiness output, planner/host/apply activation guards |
| P08-04 | `inventory/listeners.go` UDP ownership proof and per-app `UDPPublication`; `internal/apps` status and drift; `internal/diagnose`; the shared `UDPReady` check used by status and apply | Store schema, apply sequencing |
| P08-06 | `internal/apply` steps, `deploymentSteps`, lifecycle, remove, reconcile, recovery; store state transitions in commit and retirement; `internal/apps` rollback; `internal/host` wiring; atomic removal of temporary planner and consumer refusals | Spec and policy shapes |
| P08-08 | `fixtures/udp-echo/` image source; physical harness under `integration/`; `.internal/evidence/p08-udp.md` | Production packages, except fixes for defects found |

P08-03 and P08-04 both edit `inventory/listeners.go`. Land P08-03 first;
P08-04 may build its status and diagnosis output in parallel and rebase.

## Acceptance to test mapping

| Requirement | Test | Where |
| --- | --- | --- |
| No UDP intent leaves apps unchanged | Golden: existing spec, desired bytes, plan payload, decision input and unit are byte-identical with no relevant rows (`BrineState.UDP` projected away); B unchanged after A's grant | `spec`, `policy`, `plan`, `quadlet` tests |
| Incarnation is immutable and name reuse is not ownership | Head founded with the `Create` plan hash and kept by updates, rollbacks and resolution commits; remove then recreate the same name gives a new ID; a socket attributed to the name with no live head is foreign; migration backfill | `store/udp_reservations_test.go`, `plan/udp_test.go` (T22, T26) |
| Deny by default | Spec with `[udp]` and no grant, wrong exposure, grant for another app: `policy.udp_denied`, no plan saved | `policy`, `apps` service tests |
| Unknown fields, bad ports, wildcards | Parser and policy tables, including `0.0.0.0`, `::`, `::ffff:a.b.c.d`, port 80, range overlapping `app_ports` | `spec/udp_test.go`, `policy/udp_test.go` |
| Foreign ownership refuses before effects | Planner tables: equal address, IPv4 wildcard, `::`, `*`, empty owner, other app's reservation; TCP on same number does not conflict | `plan/udp_test.go` (T28) |
| Unknown observation refuses | `udp_sockets`, `local_addresses`, publication unknown give `unknown_facts` | `plan/udp_test.go` |
| Real UDP ownership has required kernel identity | Captured `ss -H -lunpe` output supplies the inode used by the existing PID/start-time/cgroup/FD proof; missing inode refuses ownership; fake command keys match the real collector command | `inventory` collector/parser tests and rootless runtime evidence (T28) |
| Intermediate merges cannot execute UDP | P08-02 through P08-04 return conflict-only plans and refuse direct host/apply requests before effects; no HTTP-only commit for UDP intent or publication without a reservation. P08-06 enables only the complete path | `plan`, `host`, `apply` and CLI tests at each merge boundary |
| Concurrent plans cannot share an endpoint | Two plans, one port: second apply stale under the lock; direct store insert hits the unique index | `store/udp_reservations_test.go`, `apply` race test (T26) |
| Exact allocation and rendering | Render goldens for IPv4 and IPv6; refusals for grant-hash, range and container-port mismatch | `quadlet/render_test.go` (T27) |
| Generated equals actual mapping | Real rootless Podman on arm64 and amd64: inspect bindings and network mode equal render | `integration/` rootless test (T27) |
| HTTP healthy plus missing or foreign UDP is not ready | Collector fakes: missing socket, foreign cgroup, extra binding, `slirp4netns` mode | `inventory`, `apps` status goldens (T29) |
| Owned socket does not imply remote reachability | `remote` stays `unverified` in every status golden | `apps` goldens (T29) |
| Restart, stop and start keep ownership | Lifecycle executor tests; stopped status shows reservation without listener | `apply/lifecycle_test.go` |
| Plan-driven transitions | Create, retain (including container-port-only), move, release only and remove produce the exact `Plan.UDP` and step list; retain inserts no row; an unsettled row of another family, including a no-`[udp]` input with a `releasing` remnant, gives `udp_reservation_unsettled` | `plan/udp_test.go`, `apply` step tests (T30) |
| Store transitions | Every legal and illegal edge on real SQLite; move commit and revert order against both unique indexes; a row owned outside the family is untouched; `RetireApp` refuses stray rows; a staged row with another incarnation is never activated | `store/udp_reservations_test.go` (T26, T30) |
| Compensation at every boundary | `stage_unit` failure after `prepare_udp` reverts and ends `Failed`; injected failure or unknown result at `prepare_udp`, `revert_udp`, `rollback_udp` and `commit`; no `Failed`, `RolledBack` or `Succeeded` with a family-owned unsettled row | `apply/executor_test.go`, `apply/unknown_test.go` (T30) |
| Endpoint-specific proofs | Healthy B plus a remnant at A refuses commit and rolls back; restored HTTP with A missing, and B lingering after restoration, end `recovery_required` with rows unchanged; unknown observation on either side; no socket is touched | `apply` tests, `inventory/absence_test.go` (T28, T30) |
| Rollback restores the selected endpoint | Historical A and current B under a grant covering both moves to A; A occupied; A no longer granted; A's row released gives a new row; release from an earlier incarnation; rollback to a release without UDP retires the row | `apps` rollback tests, `apply` tests (T30) |
| Removal leaves no socket or claim | Absence proof required; a remnant gives `recovery_required` and keeps the row `releasing`; removal recovery reads each row state | `apply/remove_test.go`, `apply/remove_recovery_test.go` (T22, T30) |
| Operation-bound resume | Kill before and after the `prepare_udp` transaction commits, and after its completion record but before `stage_unit`, resume with one row; a foreign claim or socket since planning refuses resume and reverts; checked through `InspectRecovery`, the resumed executor preflight, host `Executor.Run` and `InspectResolution` | `plan/udp_resume_test.go`, `apply/recovery_test.go`, `apply/resolution_test.go` (T26, T30) |
| Interruption at each new boundary | Real subprocess kills at `prepare_udp`, `check_udp`, `check_udp_retired`, `revert_udp`, `rollback_udp`, before and after commit and during removal; resume inspects | `apply/recovery_test.go`, real subprocess kills (T30) |
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
   reservation row and extended `ss -lunpe` evidence for the endpoint.
4. A controlled unreachable client (a port with no grant) must time out
   without any host network change.
5. Run teardown and check on the host that no fixture unit, container, image,
   grant or socket remains. Record anything retained with size and reason.

The approved disposable VPS fixture supplies amd64/public-path evidence.
Keep its single fixture app separate from the Pi's two-allocation test. Record
and remove its exact temporary firewall rule with the app; do not touch the
relay's later rule or any existing application.

Measure, do not assume: whether pasta preserves client source addresses for
UDP, its UDP flow idle timeout against the app's keepalive, and whether the
unit restarts cleanly when the bind address appears after the user manager
starts at boot (a tailnet address can arrive late).

## Recorded operator authorization and remaining boundaries

The owner approved the UDP feature and its design work. Architecture choices
above do not require another owner acceptance step.

On November 1, 2026, managing chat confirmed approval for one bounded disposable
HTTP-plus-UDP echo app on the existing VPS. Choose an unclaimed exact app/domain
under `shaulavo.dev`, use the public address and UDP 4433 if free, and install
teardown before setup. An explicit, reversible host firewall rule for fixture
UDP 4433 is approved: record it and remove it with the fixture. Keep all existing
apps, including maplestory-demo, untouched except reads. The rootless runner
and restricted app credentials gain no firewall or DNS authority.

The relay's later UDP mapping is separately approved at 4433 or the next free
port through Brine, with its exact operator grant and explicit external and
container endpoint. Apply the approved selection through the normal reviewed
operator workflow, not a generic app-controlled bind or raw runtime option.
The relay owns its later firewall rule. No new spending is authorized.

A provider cloud firewall that requires the owner's account remains an external
boundary: send managing chat the exact rule needed. Do not widen it silently.
The feature itself never edits firewalls, DNS, Tailscale or ACLs. Fixture-only
operator setup is recorded separately from Brine-managed runtime effects.

The Waffle lane still owns the reviewed split between its container bind
address and advertised public authority. That blocks public WebTransport
acceptance, not Brine's transport-only work. No Brine certificate, discovery,
authentication or protocol logic follows from it.

P08-02 through P08-06 have no external blocker. P08-03's isolated rootless
runtime test and the arm64 P08-08 drill use the Pi under D3. The bounded VPS
fixture covers amd64/public delivery and must leave no fixture app, rule,
publication, allocation, image or scratch resource behind. Unknown results stay
unverified until read back, and nothing here authorizes a reboot of the VPS.
