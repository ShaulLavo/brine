# Managed SpacetimeDB physical evidence

Recorded 2026-10-14. Phase 09 is incomplete. This record describes actual
arm64 and amd64 results through the restricted Brine command API. It does not
claim production readiness, zero loss or completion of the phase tasks.

Target names, host/account inventories, endpoints, credential values, private
keys and private report paths are omitted. Commands use uppercase placeholders
for declared names, saved plans and the protected client configuration.
Operation and point IDs below identify the exact earned results.

## Source and artifact identities

The ordinary capture/replay runs used CLI 0.1.49-dev from source
`2f8c58a2740ea3768ac1f6ffe189f51ac18f17db`, tree
`6155229ad75f4732dce440faece58c6d1969dfcb`.

| Artifact | SHA256 |
| --- | --- |
| Executed arm64 CLI | `cd9fb85bd9f3c957ace9e9ab3c9f9728b102c71564f8a308b0e7192405a279b1` |
| Executed amd64 CLI | `90797a9773246f077e42d2908d1897076362bca1e539e9b47f144a703e7cdbe2` |
| Selected patched native engine index | `d84c721e4efb07e153439f1e1e67e08569377d124017bf664c032aaa5b2f5715` |
| Selected fixture Wasm | `92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc` |
| Canonical fixture schema | `f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45` |

The engine index is published at `ghcr.io/shaullavo/brine-native-engine`.
Its native version is 2.11.0 with the independently qualified shutdown repair.
CLI/helper updates stopped the exact owned engine cleanly before replacing its
helper inode, refreshed the observer, then used the normal guarded start.
Original control, policy, material, profiles and source-preservation records
remained unchanged.

## Earned arm64 results

Normal first publication and ordinary frontend deployment succeeded through the
public preparation, module plan/apply and deployment commands. They produced a
new stopped epoch before ordinary deployment started the compatible engine and
frontend. Earlier failed publication attempts were resolved as source-preserved;
those results are separate from the successful new publication.

Chromium 156.0.8078.4 connected through the selected HTTPS/WSS routes and confirmed
its own paired write from 0 to 1. The ordinary user token was cleared and browser local
and session storage stayed empty. Management routes were denied. Publisher
material stayed outside the browser. Subsequent official SDK confirmed reads
checked contiguous paired sequences, deterministic markers and transaction times.

### Ordinary online capture and same-point real R2 test

The source began with 124 confirmed paired rows. Twenty bounded writes continued
during ordinary `backup capture`, with timestamped confirmed reads during the
capture. The final source had 144 rows and remained unchanged after the same-point
remote test.

| Receipt | Actual result |
| --- | --- |
| Capture operation | `01a13ad2128bb2a626a86672111f938b770fdf55969a`, succeeded |
| Verified point | `730609e2403f1fe9061c8ee4e7c492b8` |
| Point SHA256 | `9dce32614a2b828b3a84b1e13b0fec66b87a8b0ab8f7068ad6d8e97c3bda120b` |
| Capture duration | 35.574 seconds |
| Confirmed lower bound | 133 paired rows |
| Independently recovered final pairs | 134, an exact prefix of the current 144-row source |
| Same-point native-test operation | `01a13ad29e30a4eebcafa49a4e2cd269816271446a4b`, succeeded |
| Native-test duration | 15.967 seconds |
| Maintenance and loss | `maintenance_millis: 0`, `loss: "unknown"` |

The test downloaded the authenticated encrypted R2 archive and passively replayed
that exact point with matching module/schema. Signing recovery's encrypted-member
metadata matched the original protected role. Native and frontend writers were
not stopped for the ordinary capture. Owned upload/test workspaces and reader
containers were checked absent after success.

A verified lower bound and recovered prefix do not prove zero loss. The test
handler validates recovered pair equality, isolation and disposal before its
receipt. It does not separately persist a fresh raw native witness for every test.
The selected point/bundle witnesses, exact receipt, native-reader journal and
actual cleanup observations are the retained evidence.

### Exact preparation and remaining stopped state

A later exact preparation succeeded with 144 source rows. Operation
`01a13ad6a759eb7d8f57b9646274a4142a3cb7e83463` took 49.429 seconds and recorded
44,607 milliseconds of maintenance. Point
`7911ba25dc062f8de7d04154851cdb03` was verified, with SHA256
`275f17b7b4aae7d0f17ddbb448e72911cfcc1362f2d715fdb5af95ba019ad768`.
Engine, frontend and ingress remained stopped/fenced.

The next genuine logic-only module plan refused before an apply because the old
stopped-state guard required a live cgroup suffix after Podman had cleared it.
That guard is corrected in current source. The unused point expired at
14:45:54 UTC, leaving the original fence held. No changed-module publication,
rollback or successful abandonment has been physically recorded in this evidence.
An expired point is not new mutation coverage.

### Source52 installation and refused abandonment

The actual source52 installer completed in 15.422 seconds. Its one observer
refresh completed in 3.762 seconds. Normal Store opening reported schema 29
and preserved the original control rows. Installed source was
`a24aa0540c3b8708147fcec52836502c51595a09`, with arm64 CLI SHA256
`6a4649b6fb7d2214ca98ce8da577b61c1380dc9ebf1b8a429ce758572a3ee6e9`.
These are installed-artifact and migration results, not successful abandonment.

One actual public `backup abandon` submission ended `recovery_required` with
`policy_refused` before the host lock. The detached task had lost its protected
requester binding. It created no fence, preparation claim or desired revision
and caused no data, lifecycle or module effect. The original unused expired
point and fence, stopped 144-row source and old operation outcomes stayed intact.
No new preparation or data mutation followed.

The requester correction at `50b0091b5b2634eacbe42c9a9da3bdee892b7038`
is locally verified. It scopes the detached task from its protected input and
checks the stored accepted operation before live authorization. The correction
has not run on the physical target. Successful abandonment and ordinary resume
remain required.

## Earned amd64 results

Fresh public enrollment and exact restricted assets were installed on the
approved disposable amd64 guest lineage. First module publication and actual
engine/frontend execution are earned. Supported original-operation recovery
settled the interrupted frontend route-check path; it did not repeat deployment
or rewrite the original failed outcome. A confirmed amd64 browser write is not
recorded here.

Earlier failed online captures were inspected, reviewed and adopted without
repeated upload. Real same-point tests recovered 57-row and 82-row bounded points.
The successful new ordinary source49 capture below is distinct from those
adopted failures.

### Ordinary online capture and same-point real R2 test

The source advanced from 101 to 121 rows through 20 bounded confirmed writes during capture.
Native and frontend PIDs/state/jobs stayed unchanged through capture and test.

| Receipt | Actual result |
| --- | --- |
| Capture operation | `01a13ad10830fe24d23a37abb33e2764f531d125650f`, succeeded |
| Verified point | `4fc98f521c84aafbbb08c8ebaceadd4a` |
| Point SHA256 | `27c2e22a661467c5f744e30388c4d9b86e46fc0c8aad79025a2c9207ebf4e7ed` |
| Confirmed lower bound and recovered final pairs | 102 paired rows |
| Same-point native-test operation | `01a13ad27dc3dd6081cf9e57e180e174f5d80d4217bf`, succeeded |
| Persisted source/restored state | Equal hashes, matching point/module/schema and signing recovery metadata |
| Maintenance and loss | Zero online maintenance, `loss: "unknown"` |

Official SDK readback confirmed 121 contiguous paired rows and 120 scheduled rows,
with markers and transaction timestamps checked. Capture/test workspaces, reader
containers and per-run tunnels were checked absent. Normal guest shutdown later
returned clean native SIGINT/stop-post, clean I/O and disk check, with actual
VM/scope/process/port/temporary-alias closure. The retained read-only disk and its
backing chain remain recovery/final-delivery consumers. Full teardown is pending.

## Qualified observer scope

The earlier installed protected observer on the authorized arm64 fixture passed:

- Missing/default-false observation grant refusal.
- An explicit scoped grant and an owned empty-workspace absence check.
- A real FDSTORE sender exiting while systemd retained the descriptor, causing
	busy refusal despite sender PID absence.
- Actual manager FD closure, followed by successful absence and owned probe cleanup.

Those mechanism results remain reusable. They do not qualify every later binary
or new stopped-binding path merely because the source is reviewed. Current flow
observation and final installed-asset checks remain separate acceptance facts.

## Commands exercised

The actual restricted workflow used the following public commands. These are
scrubbed selectors, not a claim that every remaining command has run:

```sh
brine backup prepare APP --database NAME --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine data module plan APP --point PREPARED_POINT_ID --deployment-plan TARGET_PLAN_ID \
	--wasm ARTIFACTS/module-v1.wasm --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine data module apply APP MODULE_PLAN_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine plan brine.toml --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine apply DEPLOYMENT_PLAN_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine backup capture APP --database NAME --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine restore native-test VERIFIED_POINT_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine status --operation OPERATION_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine backup recovery inspect ORIGINAL_OPERATION_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine backup recovery review ORIGINAL_OPERATION_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
brine backup recovery resolve REVIEW_ID --target TARGET --config-dir PRIVATE_CONFIG --json --no-input
```

Mutation keys and secret-bearing inputs stayed in protected task records and
stdin. Accepted submissions were followed to terminal operation status. Signing
keys, publisher tokens, remote credentials and inventories are omitted here.

## Still-required acceptance

| Case | Current scope |
| --- | --- |
| Source52 unused-point abandonment and ordinary resume | Actual source52 install/schema 29 preservation and a pre-effect policy-refused abandonment are recorded. Successful abandonment/resume remain unproved. |
| Genuine logic/additive/default module updates and compatible rollback | Published artifacts and finite recovery profiles exist. Actual update/paired-client/data-preservation and rollback cases remain. |
| Destructive/access/hook/manual/stale/artifact/source/role refusals | Historical native/pure-core facts exist. Assigned current whole-operation negatives remain. |
| Live restore | Default-denied and separately granted positive restoration, new stopped epoch, original retention and compatible resume remain. |
| Controlled interruption and held-fence reboot | Current selection/head-copy/checkpoint/fsync/upload/remote-verification/publish/swap/epoch/restore/archive boundaries and supported original recovery remain. Historical restart/observer proofs do not replace them. |
| Other current fault cases | Competing locks/captures, credential expiry/outage, missing/corrupt members, metadata drift and uncertain/forced stop require their assigned bounded evidence. Reuse the [native prerequisite results](p09-runtime.md). |
| Whole-binding archive and recreate | 30-day remote/key retention, original-scoped renewal, early-expiry/default-purge refusal, authorized eligible local purge and distinct same-name recreation remain. Expiry success needs a genuinely elapsed original deadline. |
| Final delivery and cleanup | Final reviewed merged CLI/helper/observer/assets, green main and shared SQLite checks, remaining measurements, both targets' actual teardown and retained-resource owners/conditions remain. |

Source52 adds `backup abandon` for a successful initialized unused exact point,
including an expired point. It releases only that original fence after fresh
reproof and leaves all components stopped. It neither extends coverage nor starts
a service. The [operator runbook](../runbooks/spacetimedb.md#abandon-an-unused-exact-preparation)
describes this separate procedure. No manual fence, journal or clock rewrite is
part of the drill.

The [phase plan](../plans/09-spacetimedb.md) remains unchecked for P09-02 through
P09-09. Ordinary real-R2 success is earned; full physical acceptance and final
cleanup remain incomplete.
