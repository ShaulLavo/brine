# P08-04 status and diagnosis preparation

Status: partial implementation. P08-04 remains unchecked. This checkpoint stages
read-only output while P08-03 owns the shared collector files. UDP execution stays
disabled. No deployment or physical readiness claim follows.

## Implemented

- App status and release history report the committed UDP allocation. Status
  includes exact live reservation rows, incarnation and unsettled owner IDs.
  Reservation-only apps appear even before a release exists.
- `internal/udpready.Check` evaluates existing typed snapshot facts. It requires
  matching unit hashes, the exact pasta publication, one exact owned UDP socket,
  a present bind address and known unit activity. It never reads TCP ownership
  as UDP evidence. Wildcard or additional overlapping sockets refuse readiness.
- UDP local state is independent of `health.direct`. `apps.Status.Ready` requires
  both HTTP health and UDP readiness. Stopped reservations do not claim a
  listener. Missing or unsettled reservation identity stays unknown.
- The seven local reasons are closed. Remote status is always `unverified`.
  Wire decoding rejects invented reasons, null reason arrays, fabricated remote
  verification and missing UDP status for a UDP release.
- Drift includes proven UDP mapping differences without hiding separately
  measured HTTP health. Diagnosis uses the same status facts and safe read-only
  advice. It reports sockets on the committed plan's retired endpoint as
  `udp_remnant`, plus an informational operator-network reminder.
- New human, JSON and JSONL status goldens cover ready, not ready, stopped and
  unknown states. Existing HTTP-only fixtures and presentation remain unchanged.
  Nil UDP fields are omitted to preserve their existing serialized bytes.

## Reproducer and checked scope

Tests were written before implementation. The first focused status and diagnosis
runs failed because the new typed fields did not exist. The human presentation
regression then failed at runtime because UDP output was absent. Those failures
are retained in the coordinator's preparation report directory.

The following focused checks passed through the workstation's shared runner,
with a task-owned temporary directory and teardown installed before each run:

```sh
go test -p 1 ./internal/udpready ./internal/apps ./internal/diagnose ./internal/cli ./internal/dispatch -count=1
go vet ./internal/udpready ./internal/apps ./internal/diagnose ./internal/cli ./internal/dispatch
golangci-lint run --new-from-merge-base= --new-from-rev=5e6184a1 ./internal/udpready ./internal/apps ./internal/diagnose ./internal/cli ./internal/dispatch
go run ./tools/checkgate rules --baseline tools/baselines/rules.json
git diff --check
```

All evidence is deterministic typed-fixture evidence. The production status
service, diagnostic reader, dispatcher decoder and CLI presentation paths run in
the tests. The fixtures are not evidence of a real owned rootless UDP socket.

## Remaining integration

After P08-03 merges, rebase this same branch and finish P08-04 in this same PR:

1. Extend `internal/inventory/listeners.go` ownership and per-app publication
   using P08-03's actual parser types. Bind UDP to the extended socket inode,
   stable PID/start time, cgroup, descriptor, exact unit marker and runtime
   mapping. Do not infer it from the TCP publication or app name.
2. Exercise the real collector into `udpready.Check`, app status, diagnosis and
   planning. Prove missing inode, foreign process, extra mapping, unsupported
   mode, absent address and unknown observation refusal with actual command keys.
   Align stopped publication observations with the shared check.
3. Complete remnant discovery for removed apps and older released endpoints.
   Current `LoadBrineState` excludes released rows. This preparation reports a
   retired endpoint carried by the current committed plan, but does not invent
   historical endpoints for a removed head. Use the accepted retained-endpoint
   inventory/absence work rather than guessing a new store API here.
4. Finalize shared readiness with the collector proof, document the completed
   output contract, run all prescribed full gates and obtain parent-owned
   independent review. Only then update P08-04's checkbox. P08-06 alone removes
   execution guards and integrates effect sequencing.

No store schema, spec, policy, planner, host/apply guard, lifecycle, Quadlet,
shared listener/parser file, integration harness or shared plan checkbox was
changed. No host, network, secret or deployment action ran. No dependency,
version or baseline changed. All task-created test scratch was removed; shared
Go caches and pre-existing services were preserved.
