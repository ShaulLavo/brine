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
14:45:54 UTC, leaving the original fence held. Neither a changed-module publication nor a rollback
had been recorded at this point. The later abandonment result is recorded below.
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

The requester correction scopes the detached task from its protected input and
checks the stored accepted operation before live authorization. The failed
source52 operation remains unchanged. Its successful source53 continuation is
recorded below.

### Source53 abandonment succeeded

Actual CLI 0.1.53-dev source was
`4ff3ac0697518f119bf2001c7c4ec05408a9ac7a`, tree
`187bb7dbc7b919273d4ffbb409be17253201628b`. Executed arm64 CLI SHA256 was
`106ca00bbdc328dd09f53d11a1d45b5c5ace17abd85e2955f1e535a474739f52`.
PR169 merged as `9f6c467628d6a313e9da15bdb84ddf30d0c1013f` with that same tree.
The tested binary retains its actual pre-merge source identity.

Public abandonment operation
`01a13c06a7ad2a684ec0d68bd291a6079403775f7436` succeeded at 20:07:20 UTC. It
released only the original preparation fence and recorded stopped desired
revision 7. All 144 source rows stayed unchanged. Engine, frontend and ingress
remained stopped. Original point `7911ba25dc062f8de7d04154851cdb03`, SHA256
`275f17b7b4aae7d0f17ddbb448e72911cfcc1362f2d715fdb5af95ba019ad768`
and expiry stayed immutable, as did the source52 recovery-required outcome. No new preparation or module mutation ran.

The next public `start` planning request timed out after 15.061 seconds. It saved
no plan and issued no apply. A matched read-only diagnostic took 16.156 seconds:
runtime open 1.512 seconds and planning 14.523 seconds, including two inventory
passes of 4.305 and 4.274 seconds and image resolutions of 2.478 and 2.262
seconds. It reached a conflict-free plan shape with `plan_saved: false`. The inactive current-state
refusal was expected; stopped-state reading succeeded. Protected state stayed
equal and the 28.9 MB remote diagnostic was removed with cleanup checked.

Source54 correction `42d7a91ca15b202e66219fa1c1e145397ddddf7c` reuses the
collected planning input while retaining fresh apply-time preflight. For an
affirmatively stopped managed scope, ordinary start selects both engine and
frontend. Apply starts and checks the guarded engine first, then checks the exact
owned frontend under the lifecycle operation's current desired state before
starting it. Existing installed hashes, release, module and epoch stay unchanged.
The actual source54 result follows. A new exact preparation, genuine module
changes and the remaining fault/archive drills remain required.

### Source54 started both components but failed to publish ingress

Executed CLI 0.1.54-dev source was
`45a8269fc090c44f6fe22086c2b8070f0efca867`, tree
`b1397cb86c422acff3056f78fcc26109e985fe12`. The arm64 CLI SHA256 was
`c14b0fda8f9ab74dbabaa5332fbe9d5196790c38815959614250277b889b9b01`.
This was a reviewed CI binary, not a final merged binary.

The public start plan completed in 12.111 seconds. Apply operation
`01a13c426a6e6836f64558f7e6c268e07344d3041382` completed guarded engine
start/check and frontend start/check, then failed at `managed_route` with
`route_invalid`. Its terminal `failed` outcome and event sequence 22 remain
unchanged. Desired revision 8 requests both components and ingress running.

Read-only systemd and Podman observations confirmed both components running,
with no manager jobs and the current helper inode bound into the engine. Caddy
remained at generation 14 with the app route absent. No next candidate or
generation 15 was created. The original point, expiry, released fence,
successful abandonment and earlier refused operation remained preserved.
No fresh SDK count, preparation or module publication followed this failure.
The earlier 144-row count remains a historical baseline.

Local reproduction found that the completed native preparation had recorded
its deliberate ingress withdrawal from generation 13 to 14. Managed routing
facts nevertheless reconstructed the old app site from the retained route
head. The reconstructed set had ten sites; the observed set had nine. Caddy's
drift check correctly refused this mismatch. Successful ordinary ingress
resume remains unproved.

### Source56 resume, logic-only update and compatible rollback

Executed CLI 0.1.56-dev came from branch `fix/p09-withdrawn-stop` (PR172), built
with `scripts/build-cli-pair.sh` from clean trees. The final installed arm64 CLI
SHA256 was
`8262f15d98dc599959409a1756b9c848244696c3614604aa4a1610fb7fef391b`.
Each helper replacement first stopped the frontend and engine units cleanly
(engine exit 0, no OOM), because a running engine keeps the old helper bound and
reports unknown native state.

| Step | Actual result |
| --- | --- |
| Public stop on the source54 state | `01a13e146407c9d5276e4571a084bde1a5704df4c123` failed at `managed_withdraw`/`route_invalid` on the 0.1.54 helper. PR172 skips removal of a route already recorded as withdrawn. |
| Public stop, then start | `01a13e185a8a2158ec01ae13f5deb8cd1e4962f74790` and `01a13e18eda426e0d66a35c0c37dd3a8bb670bfa5029` succeeded. Ingress was published again and the SDK confirmed all 144 rows. |
| Exact preparation | `01a13e1cd35552c29ea4bd1e7a0b0dbc0d6beaa360fe` verified point `a5108468bb7dc84a02268ef7d0695bc2` with 44,127 ms maintenance. |
| Logic-only module publication | `01a13e1e4be6148f1d3f28a7d421dc69bd2dc62a0a5c` published Wasm `7cc9e994…` and activated epoch `b84f41cb3631b5b117cc9fcabe7c14ad`. |
| Frontend deployment | Planning refused until PR172 accepted the withdrawal after the completed transition. Operation `01a13e2609000e908fbd2c081219d2e39c2107b91a0c` then succeeded. |
| Data and reducer change | 144 rows were preserved. A duplicate sequence was refused with `sequence already committed`. Confirmed write 145 succeeded. |
| Compatible rollback | Planning took 21.7 seconds, beyond the old 15-second budget. Operation `01a13e2bd2d7ddaf0a2e4ff3ae48250e6c7a6864122c` restored the original frontend release. All 145 rows remained and the engine stayed on the new epoch. |
| Live restore denial | `restore native-live plan` without the grant returned `policy_refused`. |

### Source56 blockers

Adding `allow_live_restore = true` changes the operator policy hash. The active
native backup policy is bound to that hash, so `backup prepare` returned
`policy_refused`, and `backup policy plan` for a current deployment plan returned
`internal_error`. The original policy was restored byte for byte. Positive live
restore did not run.

After the restored policy, exact preparation
`01a13e312df4345f4a5bb1b8962fa9d9634602cdbe51` on epoch `b84f…` withdrew ingress
and stopped the frontend, then failed with `internal_error` while binding the
running engine for stop. It ended `recovery_required` with the fence held. The
engine unit still runs behind the withdrawn route. `backup recovery inspect`
refuses with `conflicting control record` because
`nativePreparedOriginalIdentity` accepts only two or six preparation events.
This was the source56 Pi state. Archive, recreate and the held-fence reboot did
not run. The continuation below resolves that preparation.

### Source57 preparation recovery

Clean source `60d499e` installed CLI 0.1.57-dev after a clean engine stop.
Inspection recognized source56 preparation `01a13e312df4345f4a5bb1b8962fa9d9634602cdbe51` before its engine stop intent.
Reviewed resolver `01a1401fe2fe18924732ad0dcfaa7c63602b4ee5104a` released its original fence and retained its failure.
The running source56 engine had an unlinked helper mount; its bootstrap Wasm also differed from the current published module.
Ordinary start now refuses `unknown_facts` on `managed.native_state`; the stored publisher token is expired and native metadata reads return HTTP401.
The app remains stopped with ingress withdrawn. The 145-row read and fresh prepare/abandon remain blocked on initialized publisher renewal.

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

### Merged source53 continuing capture and same-point test

The later amd64 run used the actual merged source
`9f6c467628d6a313e9da15bdb84ddf30d0c1013f`, tree
`187bb7dbc7b919273d4ffbb409be17253201628b`, CLI 0.1.53-dev and schema 29.
Normal writable Store opening migrated schema 28 to 29 and preserved every
original control row, operation, proof, fence and history.

Twenty genuine scheduled writes advanced the source from 121 to 141 confirmed
paired rows while ordinary capture and the same-point real R2 test ran.

| Receipt | Actual result |
| --- | --- |
| Capture operation | `01a13c3750ee2cc281db1b45e01062359f2f87c93875`, succeeded |
| Verified point | `9104bb71997b7fa42b688aead72bc424` |
| Point SHA256 | `524252b4a7a87fb0ff27c5d70e728616a506a88e1579c8e1c17ef8c2b497e1dc` |
| Confirmed lower bound and recovered final pairs | 122 paired rows |
| Same-point native-test operation | `01a13c378e93563644ab3d3ad2c5238e1d68fc53a860`, succeeded |
| Persisted source/restored state | Equal hashes and matching point/module/schema |
| Maintenance and loss | Zero online maintenance, `loss: "unknown"` |

The live native/frontend PIDs and protected state stayed unchanged. Capture
workspaces, isolated readers and the tunnel were checked absent. The 57-, 82-
and 102-row histories remained intact. Normal quiescence and guest shutdown
passed clean I/O and disk checks. Exact process, scope, cgroup, port, socket
and temporary SSH alias closure was verified. The latest sealed continuation
disk preserves 141 rows and the new 122-row point for remaining acceptance.

The original installer failed at a read-only startup-permit command that did
not migrate the Store. The subsequent public app-status command performed the
ordinary writable migration. A separate rootless namespace permission denial
prevented direct helper verification through `/proc`; contained Podman readback
proved the actual helper inode and hash. Neither failed installer step was
replayed. The outer wrapper also returned exit 1 after misclassifying its
watchdog's normal exit during shutdown. Actual host closure passed independently;
the wrapper failure remains recorded.

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
| Unused-point abandonment and ordinary resume | Source53 abandonment succeeded. Source56 stop/start resumed routed ingress with all 144 rows. |
| Genuine logic/additive/default module updates and compatible rollback | Source56 logic-only update and compatible rollback succeeded. Additive and default-column updates remain. Preparation after a changed-module publication is blocked (see Source56 blockers). |
| Destructive/access/hook/manual/stale/artifact/source/role refusals | Historical native/pure-core facts exist. Assigned current whole-operation negatives remain. |
| Live restore | Default denial is proved. Granting it voids the backup policy activation (see Source56 blockers); positive restoration remains. |
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


## Final Pi pass (2026-10-15)

The plain command pass started from main `57aa699`, including PRs #173 through
#177. The local amd64 client and installed arm64 helper came from
`scripts/build-cli-pair.sh`. Confirmed SDK reads checked the fixture data through
the managed route. Fixes are in [PR #178](https://github.com/ShaulLavo/brine/pull/178).

1. Publisher renewal and reviewed activation passed. Start `01a1410a9fa5aeb9aea56b76c6f9a8f6fb89e2fd3311` succeeded, the frontend returned HTTP 200, and the SDK read 145 rows.
2. Online capture `01a1410b72d3f2c2a14e2c56c3cd54c0c2490f7484a8` verified point `287fc6da1d745e17eb933ee10775a47f` with zero maintenance. Native restore test `01a1410c563d0fc3868f3e1412139426f051fb2a51be` verified that same point.
3. Additive publication `01a141506df718371efc0c21650f40d5e41ba17ef078` and deployment `01a1415259a9b3ac5889c461355bc23bdd054485bfa9` passed with 145 confirmed rows. The supplied default-column artifact removes the additive table. The coordinator approved restoring the original schema between those updates. The schema-removal guard remains intact; the default update has not run.
4. Live-restore policy activation `01a141b74a858ef445c2f35f5af08525ce35bbb7456f` passed. Exact preparation `01a141d159a78e0ef86ab4b84bbd57b2a6a619206682` verified point `14034a1d61e56adc8147f4e87547348c`. Restore `01a141d241805cc8760d4a786048d7e2cba23c1e5b97`, selecting pre-additive point `287fc6da1d745e17eb933ee10775a47f`, stopped at `transition_intent`, sequence 4. Its target is staged, but no engine-transition input exists. The supported resolution review refuses at that missing transition. No successful live restore or original-policy reactivation is claimed.
5. Held-fence reboot has not run. Unused preparation abandonment did pass independently, including operation `01a141cf455401ac0b380084624942b18ae4d2250267`, which released an expired unused preparation and left components stopped.
6. Native archive and same-name recreation have not run.

Frontend recovery passed on the Pi. Resolver
`01a141b0ab6eb5f3be832408ed60c390912cd5a771b8` restored the previous database-only
release through Brine's compensation steps. The original operation and failed
resolver remain in the journal. The actual frontend failure was its fixture's
single-marker equality check. While deployment was active, its startup guard
passed and containers repeatedly started, then exited. Running the immutable
image with Brine's accepted-marker list reproduced the exit and error. The
permit refusals came after the operation became terminal.

The corrected fixture was published by [the fixture workflow](https://github.com/ShaulLavo/brine/actions/runs/38818306871)
for both architectures. Its v1 image index is
`sha256:4037148fcb4c4b1ddee3b19c97d7d8db87c847fc4768d5d93ab4f3f5013245cf`.
Deployment `01a141d08b3ef58c0726a7a75896d8baa3cfbb02dc53` succeeded with this
image, returned HTTP 200, and supplied 145 confirmed SDK rows before the next
preparation stopped it.

The continuation also fixed restore checks that mistook the historical serving
receipt for current ingress. They now inspect the preparation's recorded
withdrawal against Caddy. Quiescence recognizes an unloaded failed service only
with zero PIDs, no cgroup and no job. Incoming repository initialization now
shares a transport budget that ends before its material scope expires. The
restore previously passed a 15-minute job context to a 10-minute repository
scope and was refused before transport admission. Focused regressions failed
before these fixes and passed afterwards.

The last restore's repository journal contains `init_issued` without
`init_unknown` or an initialized result. The current source remains stopped and
fenced. Preserve its download, staged target, repository lease and original
records. Do not repeat apply, clear its fence, or edit control records. The
[phase plan](../plans/09-spacetimedb.md#p09-08-run-the-physical-pi-and-browser-recovery-drill)
records the unresolved pre-transition recovery path. The temporary live-restore
policy grant remains in place for that recovery; original policy restoration
and backup reactivation remain required afterwards.

Earlier fixes in this pass cover preparation after material renewal, recovery
identity after a later release, schema-marker conflicts and refusals for changes
outside managed publication. Original fixture inputs remain preserved. Full
Phase 09 acceptance remains incomplete.
