# Phase 09: Managed SpacetimeDB

**Status: Approved.** Decision [D13](../DECISIONS.md#d13-spacetimedb-is-a-managed-data-engine-alongside-sqlite) governs this phase. No task is complete because this plan exists.

## Goal and delivery limits

Deploy a browser game with a managed standalone SpacetimeDB engine on an enrolled Linux server. Preserve SQLite's guarantees and prove recovery from generic S3-compatible storage. Serve Bevy/Wasm assets with ordinary HTTPS and SpacetimeDB HTTP/WebSocket through Caddy. A separate Rust application service is optional. WebTransport and a full Bevy integration are not prerequisites for the data engine.

Use one app-owned standalone engine tree and one module database per binding initially. Admit only a licensed production topology. Managed cloud provisioning, credential minting, multi-tenant hosting, HA, arbitrary commitlog point-in-time recovery and unverified live-copy behavior are outside this delivery. Routine backups use online native snapshot/log archival with existing encrypted backup tooling. Accept explicit maintenance downtime for exact fenced data preparation and reviewed publishing. Fail closed where upstream behavior remains unproven.

## Existing implementation and first boundary

The shared typed engine boundary and public managed commands are implemented.
They include stopped allocation, private roles, backup policy, online capture,
exact preparation, isolated native restore tests, reviewed module publication,
separately granted live restore, native archive and maintenance, and unused-point
abandonment. Cobra and the restricted dispatcher call the same recorded engine.
The [operator runbook](../runbooks/spacetimedb.md) describes their sequence.

The [physical evidence](../evidence/p09-pi.md) records first publication on both
architectures, a Pi browser write, and ordinary continuing-write capture followed
by same-point real R2 tests on arm64 and amd64. Those earned results do not
complete the remaining update, restore, archive, fault, final-delivery or cleanup
acceptance. The task boxes below remain unchanged.

D11/D12 and Phase 04 remain authoritative for SQLite. P09-01 extracts the smallest typed engine boundary while keeping SQLite output and behavior unchanged. It can begin against the merged foundations without waiting for every Phase 04 checkbox. Each later task requires demonstrated shared guarantees, not an unchecked phase label or package mocks. Missing prerequisites become explicit blockers in that task's PR. Coordinate shared store/spec/host edits with Phase 04 owners.

## Research baseline and open gates

Research checked on 2026-10-10 against [v2.11.0, released 2026-10-07](https://github.com/clockworklabs/SpacetimeDB/releases/tag/v2.11.0). D13 records the official image index and both platform digests, pinned source links and license text. Arm64 binaries and image metadata exist. The original planning PR pulled no image and ran no service. A later [arm64 online backup pilot](../evidence/p09-online-pilot.md) remotely restored four encrypted native snapshot/log captures while the source kept writing. Fixed metadata and one complete active-head copy are proven pilot scope, not production admission.

Resolve these facts in P09-02 before production admission:

- Independently verify Docker Hub's index and platform manifests, image UID/GID, rootless operation and SIGINT forwarding through the container entrypoint. Preserve the pinned version and SBOM/provenance where available. If registry metadata differs, stop and review the pin rather than updating it silently.
- Establish whether acknowledged reducer results always survive the configured clean shutdown. The internal durability queue's flush/sync behavior is not a public whole-engine barrier. Test immediate stop after acknowledgement, active subscriptions, scheduled reducers, SIGTERM, forced kill and OOM. Unknown outcomes cannot become exact pre-mutation restore coverage.
- Prove online native snapshot completeness, durable-offset selection, required log suffixes and bounded active-head handling, including deliberately partial records. Validate restored Sled control DB, program bytes, metadata, configuration and signing material under a coordinated immutable metadata checkpoint. Exercise concurrent rotation, segment rewrite/compression, compaction and metadata mutation. Bind captured owned descriptors/content, never assume a sealed path is immutable. The internal path hierarchy is explicitly unstable. An individual database snapshot is not a whole-instance backup.
- Select and prove a fixed canonical schema/module observer and application marker contract. Use version-pinned metadata and typed read-only checks; no supplied SQL or arbitrary shell. Prove that observation does not run a reducer or client/module hook. Routine SQL can trigger hooks. A private owner-only module/schema metadata observer is a candidate, but schema JSON must be normalized and stable across restart before its hash is authoritative. Fresh allocation, initialized state, missing prior data and unreadable/corrupt data remain distinct.
- Verify the `pre_publish` plan/token's source binding, expiry/staleness behavior and deterministic noninteractive access. Probe defaulted-column additions against this version. Do not treat conflicting moving-doc examples as an API contract.
- Measure RAM, archive size, write-driven disk growth, restore replay time and online capture cost with a bounded fixture on both architectures. The official image is roughly 0.9 GB compressed per platform; the Pi must have capacity before pulling. Record the exact workload, never advertise Pi timings as production capacity.
- Define production auth with operator-provided JWT/OIDC configuration and publisher ownership. Test that Caddy's required-route allowlist excludes publish, database creation, SQL and other management routes. Establish recoverable signing-key delivery without backup credentials or owner tokens reaching game clients.
- Obtain a protected license-use attestation. The pinned grant limits an application/service to one production instance. Do not deploy a multi-instance production topology while this is unresolved.

If any exact-capture or observer gate fails, leave managed mutation admission disabled and update D13 with the evidence. A successful `/health` request is not a substitute.

## Tasks

### P09-01. Extract the typed engine boundary without changing SQLite behavior

- [x] **P09-01**

**Dependencies.** Merged Phase 04 identity/allocation, durable fence/permit, credential and restore-source foundations. Existing implementation must pass its regression suite. Full Phase 04 physical exit is not a prerequisite for this behavior-preserving extraction.

**Work.** Define tagged engine declarations, schema observations, backup capabilities and restore sources. Share allocation IDs, destination/epoch reservations, credential references, retention bounds, fences, journal and immutable receipts. Give the engine adapter bounded operations for observation, quiescence/capture and isolated verification. Keep Litestream runtime/barriers and SQLite catalog/PRAGMA logic behind the SQLite variant. Migrate all existing callers in one change; add no old-shape fallback. Unknown engines or incompatible receipts refuse before effects. Preserve the existing SQLite public spec by defaulting its existing declaration to the SQLite engine at parsing, not by runtime guessing from paths. Do not expose a SpacetimeDB spec that lacks its safety gates.

**Acceptance.** Existing SQLite TOML, JSON plans, schema fingerprints, restore receipts, initialization, credential activation, fence/reboot semantics and archive reservations remain correct. An engine mismatch cannot consume a SQLite restore point or start a service. A SpacetimeDB declaration does not require a `.db` filename, LTX TXID or Litestream unit.

**Evidence.** T34 is implemented by the closed-codec, ownership/null, unsupported-operation and cross-engine permit/source tests, plus `scripts/check-engine-boundary.sh`. Independent review reproduced and verified fixes for caller-owned evidence and required-null gaps. Persistent allocated/unknown desired and plan goldens and a checked SQLite receipt match actual pre-extraction bytes. Full `go test ./...`, Linux and darwin/arm64 vet, CLI build, lint, authority/dead-code/clone checks and focused race tests pass. No physical SpacetimeDB claim is made in this task.

### P09-02. Prove the pinned standalone runtime and capture prerequisites

- [ ] **P09-02**

**Dependencies.** P09-01. Test harness uses disposable local roots and pinned release artifacts; no production host access.

**Work.** Resolve the research gates above, extending the online pilot with completed-snapshot/durable-offset proof, partial active-head records, concurrent rotation/compaction and metadata checkpoints. Build a bounded reproducible probe and write a scrubbed runtime/capture contract with exact version, digests, commands and observed results. Use the official image or a reviewed source build from the exact tag with pinned dependencies/base images. Fix SIGINT stop forwarding and bounded timeout semantics. Define resource declarations, canonical schema observer, recovered marker, secret recovery dependencies and the distinct app/server permits. Keep managed mutation admission disabled until exact quiescence and replay are proven. Routine backup proof follows the online native snapshot/log route, not mandatory stop/copy.

**Acceptance.** An online snapshot/log capture with a consistent protected metadata checkpoint privately replays with the expected database identity, module and explicitly confirmed committed marker. Partial heads and concurrent rotations either recover a proven bounded point or refuse without a receipt. Metadata drift refuses. Unknown/forced shutdown never yields a verified exact point. No startup path bypasses the fence. Runtime and observer results distinguish missing historical data from affirmative fresh-empty allocation. Both architecture manifests are verified; actual arm64 runtime evidence follows in P09-08 if unavailable locally.

**Evidence.** T35 covers this task. Add TEST_MATRIX standalone runtime cases for immediate acknowledged-write stop, active WS, scheduled reducers, signal forwarding, stop timeout, corruption, incomplete snapshots, durable offsets, torn active-head records, concurrent rotation/compaction, metadata drift and private replay. Record finite test budgets and exact expected marker values. Document remaining unproven durability semantics and refuse dependent admission rather than claim zero loss.

**Delivered prerequisite slice.** [P09 runtime evidence](../evidence/p09-runtime.md) and the [manual repository gate](../../tools/spacetime-runtime/README.md) retain actual native/OCI, online-prefix, metadata, shutdown, guarded-bootstrap, publication and JWT facts. The shared Go canonicalizer and hook-free metadata observer are qualified without managed admission. Canonicalizer v2 fixes reducer-set ordering; the pilot v1 hash remains historical only. Actual program hashing is Keccak-256 with a reversed u256 JSON wire representation, not BLAKE3. The retained repository gate recovers 180 explicitly confirmed rows with active history versus 135 from closed history, and refuses missing-history reconstruction when bootstrap bytes are withheld. Its nonzero source stop earns no exact point.

P09-02 stays unchecked. Managed startup provenance, fences, the exact-state reader and public capture/preparation are now composed. The [physical evidence](../evidence/p09-pi.md) records their earned scope and remaining current fault cases. Unknown stops still cannot supply exact preparation. The earlier helper-only gate does not itself grant production authority.

### P09-03. Publish a reproducible multi-architecture game-data fixture

- [ ] **P09-03**

**Dependencies.** P09-02's pinned runtime and observer contract.

**Work.** Add `fixtures/spacetimedb-app/` with a tiny Rust SpacetimeDB module compiled to Wasm, a public evidence table with sequence/marker/commit-time, a fixed read-only schema marker, and authenticated bounded write reducers. Include a browser subscription client plus a noninteractive drill client. Require explicit confirmed reads for recorded recovery markers and test that unconfirmed acknowledgements cannot supply durability evidence. Keep real game code and Bevy builds out of the recovery gate. Include an additive reviewed module revision, a rejected destructive revision, and a scheduled-reducer case to prove the engine remains a writer without the browser. Pin the Rust toolchain, module SDK, CLI and client SDK; retain lockfiles and module Wasm SHA-256.

Add `.github/workflows/spacetimedb-fixture.yml`, following `fixtures/sqlite-app` and `.github/workflows/sqlite-fixture.yml`. Use SHA-pinned Actions, minimal permissions, finite budgets and GitHub Actions publication to GHCR. Publish a fixture image for `linux/amd64` and `linux/arm64`, record its immutable index/platform digests and source revision, and retain the module artifact digest. The app fixture never publishes a module on startup. Initial publication belongs to the separate reviewed-data operation. Publish a small app image, not an extra full Rust compiler image; use the separate pinned official engine image.

**Acceptance.** Both architecture images serve the same browser fixture. The client records an acknowledged marker before capture and observes it after remote restore. Publish a deterministic module from the retained digest without building on the enrolled host. No owner token, signing private key or S3 secret exists in image layers, Wasm, static assets or workflow logs.

**Evidence.** T36 covers this task. Add TEST_MATRIX fixture cases for artifact identity, read-only observation, denied writes, scheduler behavior and browser WS events. Record GHCR immutable digests from the workflow, not invented references. Test both architectures or report the arm64 execution gap until P09-08. A local-only build does not satisfy publication acceptance.

### P09-04. Integrate managed engine planning, permits, auth and ingress

- [ ] **P09-04**

**Dependencies.** P09-01, P09-02, P09-03; working enrollment/runtime and D12 compatibility enforcement.

**Work.** Add strict engine declarations and immutable desired/observed facts for image/version, runtime identity, owned data tree, database/module identity, schema compatibility, resources and secret references. Represent the engine service separately from the optional application container. Reserve loopback host ports and render typed Quadlets/Caddy config. Expose only approved client HTTP/WS routes for the selected database, never wildcard management access. Deliver operator-provided publisher/signing/OIDC material privately with references in state. Add displayed license-use admission and explicit online-backup cadence and exact-preparation maintenance policy. Default cadence proposals are 24-hour captures and seven-day drills; application/server plans must disclose online capture cost, exact-preparation downtime and loss budget before apply.

**Acceptance.** Validate/plan are read-only. Existing data is never initialized or published by deployment. App and engine startup refuse held/missing/inconsistent permits. Auto-restart, reboot and reconciliation cannot reopen fenced ingress or start the engine. Another app cannot read its data or secrets. Public/personal-tailnet visibility remains independent of publisher access. Optional Rust service changes do not imply module changes. Cadence and credential changes affect backup scheduling only unless a signing-key change explicitly requires planned engine restart.

**Evidence.** T37 covers this task. Extend TEST_MATRIX authority and persistence cases with route allow/deny tests, WS forwarding, identity ownership, secret redaction, signal/resource rendering, license admission, compatibility rollback and restart permits. Full fake-host tests precede P09-08 physical routing/reboot evidence.

### P09-05. Capture immutable remote backups and verify isolated restore

- [ ] **P09-05**

**Dependencies.** P09-04; proven atomic create-only S3 upload, credential scope/expiry and retention evidence from Phase 04; P09-02's capture contract.

**Work.** Add D13's routine online capture and distinct exact preparation operations. Select completed native snapshots and required log suffixes plus a bounded active-head copy under P09-02's proven contract. Coordinate immutable control/module/configuration/signing metadata checkpoints, rejecting drift and unproven compaction/rotation states. Routine capture keeps source writers and ingress running. Stage/fsync/hash the bounded private capture and use existing authenticated encrypted backup tooling, such as the tested restic route, for generic S3 upload and remote download. Bind the exact tool/repository snapshot and member manifest, capture boundaries/durable offsets, module/schema, secret recovery version and reserved epoch. Do not build a custom uploader/log shipper or require host filesystem snapshots as the baseline.

Private signing material must be encrypted with a separately recoverable operator-supplied key, or reference an independently tested external immutable secret version. Never upload plaintext signing keys, owner CLI tokens or the recovery key. Do not mark a point recoverable until authenticated remote download, complete member checks and isolated replay pass. Interrupted tool operations cannot produce a successful receipt.

For exact pre-publish, live-restore and retirement preparation, hold the host lock, journal the fence before closing ingress/stopping every writer and independently inspect processes, restart jobs and lifetime locks. Bind a fresh remotely verified capture to that exact quiesced source and consistent metadata checkpoint. Retain the fence through the reviewed operation. Unknown stop or unproven coverage refuses; an ordinary recent online point cannot substitute.
Use externally supplied generic S3 credentials with endpoint/region/path-style settings, optional session token and lifetime budgets. Credential replacement resumes only eligible backup work; it cannot release a data fence or restart a live engine. Bound temporary bytes, archive expansion, upload time and verification memory. Validate archive paths/types/modes and stop at quota/capacity failures without deleting live data.

**Acceptance.** Real remote storage, not a file replica or signing mock, returns the exact complete bundle. The isolated same-version engine has no live mounts, public route, external egress or destination-write credentials. It replays expected module/schema/identity/marker and read-only invariants. Report last verified capture, missed schedules and observed/unknown loss window. Stale points, unresolved stops, corrupt/missing objects, expired credentials and unrecoverable signing material refuse. Upload interruption never creates a successful receipt. Local source drift invalidates prepared coverage.

**Evidence.** T38 covers this task. Add TEST_MATRIX backup/restore cases for tree traversal/symlink attacks, size limits, create-only collisions, partial upload, hash mismatch, signing-key recovery, session token expiry, missing retention evidence, storage outage and concurrent captures. Include process interruption during snapshot/log selection, bounded head copy, metadata checkpoint, staging fsync, encrypted upload, remote verification and exact fenced preparation. Exercise snapshot/log rotation and metadata changes during capture. For exact pre-mutation points, compare explicitly confirmed source evidence at quiescence with the independently replayed marker and refuse uncertain coverage. Real S3 evidence is required for task completion; scrub all endpoint/inventory/secret details.

### P09-06. Publish modules through reviewed initialization and migration plans

- [ ] **P09-06**

**Dependencies.** P09-05's exact quiesced point and restore receipt; D12 reviewed-data authority, compatibility and rollback policy; P09-02's deterministic `pre_publish` contract.

**Work.** Add explicit planned create/publish operations, never deployment hooks. Bind app incarnation, engine/database, source/destination schema, Wasm digest, engine version, preflight plan hash/token binding, operator review, bounded validation and exact restore coverage. Fresh-empty create first captures/restores the empty server allocation and independently proves no previous database history. D12's single restricted-channel identity applies reviewed plans for initial create and every module update. No separate agent migration grant is added. Review init/update reducers and effects even for a logic-only update.

Publish on a private restored staging copy with no external egress, validate module/schema/data invariants and designated app recovery releases, then stop it. Under the retained live fence, journal a same-filesystem bundle swap, preserve the original and create a fresh backup epoch. Do not start a fenced live engine to gain access to its publish endpoint. A known operator-reviewed transform is separate from generic shell. Reject `--delete-data`, `on-conflict`, unrestricted `--yes` and unmanaged major engine upgrades. Breaking schema changes require a separate operator procedure; no compatibility code for stale browser versions.

**Acceptance.** Safe additive update preserves markers and expected schema. Destructive/manual migration preflight refuses before live effects. A forged marker, stale source, expired/rebound preflight token, changed artifact or missing compatible recovery release refuses. Failed/unknown publish observes current module/schema before resolution and never blindly republishes. Rollback changes app code only and cannot restore an incompatible writer. Initial allocation does not silently become initialized on ordinary app start.

**Evidence.** T39 covers this task. Add TEST_MATRIX reviewed-data cases for new database creation, logic-only update reducers, defaulted columns, nonempty table removal, constraint/access changes, no-init deployment, failed publish, staged verification, stale review, source drift and crashes before/after swap/epoch commit. Keep SQLite initialization and rollback tests green. Document the actual noninteractive CLI/API boundary and demonstrate that no token enters argv, logs or public state.

**Delivered implementation.** The earlier [schema/publication core evidence](../evidence/p09-schema-publication.md) records the pure comparison and cursor slice. Protected extraction, role lookup, remote preparation, private publication/projection, source preservation, swap/epoch transitions and public task/inspection/resolution commands are now composed. First publication has run on both architectures. Temporary composition allowances and obsolete APIs were removed. Genuine changed-module application, compatible rollback, refusals and controlled publication/swap/epoch recovery remain required by the acceptance above. P09-06 stays unchecked. Existing-name remapping still requires a separate operator procedure.

### P09-07. Complete restore, archive and interrupted-operation recovery

- [ ] **P09-07**

**Dependencies.** P09-05, P09-06; D8/Phase 04 archive, purge and live-restore authority and recovery primitives. Share their proven implementation, not merely a plan checkbox.

**Work.** Extend removal/archive/purge and separately gated live restore for the whole engine binding. Prepare a fresh current restore point before replacement/removal. Preserve original tree and recovery secrets, all remote epochs and 30-day retention. Swap only a verified private restored tree. Use a new epoch and independently check schema compatibility before resume. Reconcile capture, publish, restore and archive journals by actual source/staging/archive identities, processes and module observations. Recovery resolution never retries an unknown destructive action or clears an unexplained fence.

**Acceptance.** Remove/recreate receives new identities and prefixes. Expiry/purge cannot touch live or reused paths. Restore never appends into the old epoch. Credential renewal remains possible for archives without granting provider admin access. Reboot at any interrupted step leaves live engine/app starts blocked until state is known. An operator runbook covers manual recovery when automated resolution cannot prove safety.

**Evidence.** T40 covers this task. Extend T19/T22-T23 with full-tree restore, archive retention, remove/recreate/purge and interruption cases for archive rename, bundle swap, key dependency, epoch activation and fence release. Exercise scheduler/restart races and timed-out stop/upload/publish. Unit/fake tests do not replace the P09-08 drill.

**Open blockers found 2026-10-15 (Source56 Pi pass).** Details and operation IDs
are in the [physical evidence](../evidence/p09-pi.md#source56-blockers).

1. *Preparation after a changed-module publication fails.* On the Pi, after a
   logic-only publication activated a new epoch and the frontend was redeployed,
   `brine backup prepare APP --database main` withdrew ingress, stopped the
   frontend, then failed with `internal_error` inside the engine stop binding
   (`bindNativeStopAttempt` in `internal/host/native_stop_linux.go`). Start with
   `verifyEngineService`, `verifyEngineLoadedService` and `bindEngineNative`
   against the new epoch's runtime and material paths. A debug build showed only
   the store's `conflicting control record` for the follow-up inspection.
   Add a fake-host regression that prepares after a completed publication.
2. *That failure cannot be resolved.* `nativePreparedOriginalIdentity` in
   `internal/store/native_backup_resolution_prepared.go` accepts only two or six
   preparation events, so `backup recovery inspect` refuses an operation that
   failed before recording its stop. Inspection must recognize "fence held,
   ingress withdrawn, frontend stopped, engine stop never issued" and allow a
   reviewed resume that republishes ingress without a new preparation key.
3. *An operator policy edit voids backup activation.* Changing the operator policy,
   for example adding `allow_live_restore = true`, changes its hash. Preparation then
   refuses with `policy_refused`, and `brine backup policy plan --deployment-plan ID`
   returned `internal_error` for a current plan. Live restore requires such an
   edit, so define and test the re-activation path before running it.
4. *Helper replacement requires a stopped engine.* A running engine keeps the old
   helper bound, and planning reports `managed.native_state` unknown until the
   operator stops the units. Document this in the upgrade runbook or make stop
   planning accept it.


5. *Initialized publisher renewal blocks the Source57 continuation.* The stored
   publisher JWT is expired. Native metadata observation with that exact role
   returns HTTP401. After clean helper replacement and successful reviewed
   release of the source56 preparation, `brine start APP --target TARGET --json
   --no-input` returns a conflict with `unknown_facts` on `managed.native_state`.
   No start apply or new preparation ran. `data material set` supports the
   unpublished allocation, so implement a reviewed, versioned publisher-role
   renewal for an initialized binding without changing signing roles or editing
   the control DB. Then resume through ordinary start, confirm 145 rows, and run
   one prepare/abandon. Source57 operation IDs are in the physical evidence.

### P09-08. Run the physical Pi and browser recovery drill

- [ ] **P09-08**

Basic Pi command pass, 2026-10-15. Evidence is in [the final pass](../evidence/p09-pi.md#final-pi-pass-2026-10-15).

- [x] Renew publisher material, activate it, start, and read 145 confirmed rows.
- [x] Capture an online point and verify its remote restore.
- [x] Publish the additive module and confirm all 145 rows remain.
- [x] Recover the failed frontend through Brine, deploy the corrected fixture, and confirm HTTP 200 with 145 rows.
- [x] Publish the default-column module after restoring the original schema. Confirm 145 rows and `rating = 7` with the matching client.
- [x] Complete live restore, restore the original policy, and reactivate backups.
- [x] Reboot while prepared, prove the fence holds, abandon, and start.
- [x] Archive the native binding and check same-name recreation when supplied material permits it.

**Archive continuation, 2026-10-16.** [PR #180](https://github.com/ShaulLavo/brine/pull/180)
completed the basic archive and same-name recreation pass after #179's restore
and preparation fixes. The [archive evidence](../evidence/p09-pi.md#native-archive-continuation-2026-10-16)
records the exact commands and operations.

The earlier archive refusal came from these incoming reservations without native
epochs. Each already had a successful `source_preserved` resolution:

- `01a13934698e32877fc2f2f78ed3597a9c276871c156`, incoming intent `c81a0c32c81246ab8fbbe62ee8956eaa`, had no lease or initialization event.
- `01a1394cb28583e3d3bd1ea97a020a2f74f15224d2f2`, incoming intent `2ef1431d08b41368e7ceb9e871d1c6e8`, had no lease or initialization event.
- `01a1396c9852935f9a85f7893d036669cc02119edeaa`, incoming intent `488e73b6fab8f7fe8bdce99a7e444954`, had an initialized repository but no engine transition.

Archive now joins those resolution proofs, retains their remote prefixes and moves
their private targets. Archive `102470420a1ebbac3efa5350c44e4fa7` is retired with
retention through `2026-11-15T05:47:04.95797836Z`. Its nine epochs and eight targets
remain retained after recreation. Current backup health reported `verified` for
point `518de8a360ee4c598364c857a127c604` without changing historical failed tasks.

The original spec recreated the same name with a new incarnation, database,
binding, epoch, remote prefix and native database identity. Supported resolution
completed the new deployment after the route receipt was interrupted. Engine and
frontend are running, status is healthy and HTTPS returns 200. Historical port
reservations and route ownership no longer block the new incarnation. Full
P09-08 remains unchecked because the broader acceptance cases below are incomplete.

Frontend recovery is no longer the blocker. Brine restored the previous
database-only release through its compensation steps, and the corrected fixture
then served HTTP 200 with 145 confirmed rows. An unloaded failed frontend with
zero PIDs, no cgroup and no job also no longer blocks unused preparation
abandonment. No control records or units were manually edited to produce these
results. Exact operation IDs and qualification limits are in
[the final evidence](../evidence/p09-pi.md#final-pi-pass-2026-10-15).

**Dependencies.** P09-03 to P09-07, a published multiarch fixture, real scoped S3 test destination/retention evidence supplied by the operator, and coordinator-supplied Pi client configuration.

**Work.** Use D3's standing Pi approval. Preserve remote access and back up operator-owned files before edits. Verify destination mount/free space before engine image pulls and restore staging; keep heavy local jobs in the shared queue. Deploy only disposable fixture state. Run actual arm64 rootless Quadlets and Caddy, create/publish with reviewed plans, connect a browser subscription, commit explicitly confirmed known markers, perform online native capture/encrypted upload/restore-test while writes continue, replace app code, perform the admitted module update and reboot. Run a separately authorized-by-policy fixture live restore. Cover S3 outage/credential expiry, competitor locks, signing-material recovery, stop timeout, partial active-head records, concurrent rotation/compaction, metadata drift and interrupted capture/publish/restore/epoch activation. Confirm app and engine permits block boot while fenced. Run the same bounded core capture/replay drill on disposable amd64 infrastructure if available; without actual amd64 execution, report that release blocker.

**Acceptance.** Arm64 runtime works, known markers survive replacement/reboot and independently restore from real S3. Public clients cannot publish/create/query arbitrary SQL. Browser HTTP/WS behavior and owner authorization are demonstrated. Unknown stop and crashes preserve fences, source data and recovery paths. Record image/module digests, observed loss window, RAM/disk/capture/replay measurements and scrubbed command/receipt evidence. No claim of zero loss or production readiness follows merely from one drill.

**Evidence.** T41 covers this task. Add a SpacetimeDB section to TEST_MATRIX's physical exit gates and commit a scrubbed `docs/evidence/p09-pi.md` with exact commands/results and negative cases. Include actual GitHub fixture publication and arm64 manifests. Provide a tested remotely reachable private fixture URL if the owner is asked to inspect the browser; do not ask them to view this host's screen. This task is covered by D3, not permission to alter other hosts.

### P09-09. Deliver operations documentation and phase exit evidence

- [ ] **P09-09**

**Dependencies.** P09-08 and passing SQLite regression/physical recovery evidence for the shared engine boundary.

**Work.** Update CONTRACTS, architecture, CLI/JSON examples and operator runbooks for explicit engine selection, downtime/cadence, private publish authority, license-use bounds, resource sizing, key recovery, retention, scheduled backup failures and separate database/app rollback. Explain SQLite streaming versus SpacetimeDB online native snapshot/log archival, its proved boundaries and separate exact fenced preparation. Document unsupported live-copy states, HA and arbitrary point-in-time recovery without promising follow-on compatibility code. Record exact pins and re-run gates before engine upgrades. Remove obsolete SQLite-only product scope wording only where behavior actually ships.

**Acceptance.** A fresh agent can deploy the fixture, inspect backup health, prepare a reviewed publish, run isolated restore, recover an interrupted operation and remove/archive it through typed Brine operations without raw root access. Machine output identifies engine, exact point, module/schema, capture cadence, exact-preparation maintenance interval and bounded/unknown loss. No docs claim Maincloud backup behavior for standalone. Remaining blockers are explicit and unchecked.

**Evidence.** T42 covers this task. Add command-contract goldens, authority refusals and cross-engine regression coverage to TEST_MATRIX. Re-run repository checks and retain both architecture acceptance evidence. Only then mark this phase's exit complete.

## Execution and verification

One task per reviewable PR where practical. Keep shared contract changes with their implementation. For code tasks, write failing behavior tests first, then run repository-required `go test ./...`, `go vet ./...`, `go build ./cmd/brine`, formatting and `git diff --check`; add race tests for scheduler/permit concurrency. This machine's heavy jobs run through the coordinator's shared build queue, without exclusive quiet mode for ordinary tests. Fixtures need their own pinned Rust/client tests and image checks. Expected TEST_MATRIX additions above are obligations, not tests already present.

P09-01 is the first implementation task. P09-02 then fixes the engine-specific contract. Fixture work precedes physical evidence, and exact remote restore precedes any publish or live replacement. New operations cannot borrow a SQLite-only receipt to bypass these dependencies.

## Exit gate

The published fixture runs on arm64 and amd64 with observed resource bounds. Browser subscriptions work through approved Caddy routes. Known data survives app/module changes and reboot. A real isolated remote restore reproduces module/schema/identity and committed markers, and crash recovery preserves fences. SQLite has no regression. Production admission respects the pinned license and accepts the displayed maintenance and loss budgets. Every data operation has fresh exact restore coverage or refuses. Tasks stay unchecked until that evidence exists.
