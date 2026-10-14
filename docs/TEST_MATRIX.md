# Brine acceptance and failure test matrix

**This is a test plan, not a report of passing deployments.** No real VPS is authorized by this file. Unit tests use synthetic fixtures and fake adapters; destructive integration tests require an explicitly named disposable target and clean test credentials.

P09-04B pure preview checks cover shared managed database syntax, paired optional
frontend, normalized ENGINE/URL/NAME/ACCEPTS, module SHA-256 versus executed native
Keccak/API-wire pilot witness, bounded confined client module/schema resolution,
production v2 schema hashing without marker authority, explicit license/cadence/loss facts, resource ceilings,
secret-purpose separation, independent ports and typed client-route denies.
At this pure-preview prerequisite stage, managed plans are conflict-only with
zero executable changes. Renderer
checks cover the typed Podman supervisor service and optional frontend Quadlet;
an explicit pinned local Caddy CLI can independently adapt both route previews.
Optional actual Caddy routing probes compare the pure checker with forwarding
to private HTTP stubs, covering literal/escaped/cleaned paths, duplicate and
encoded token keys, SDK query fields, malformed encoding and terminal original
managed-prefix denial before frontend fallback. Caller input mutation must not
change an already hashed managed plan.
These checks do not complete T37 or verify physical routing, startup, publishing,
backup, fence handoff or reboot.

Managed engine identity checks distinguish recorded history from new allocation.
A retained runtime and plan with a non-current pinned image decode without
changing canonical bytes. An unchanged deployment stays bound to that identity.
Fresh preparation and initial material refuse an unselected image or mismatched
platform manifest. Descriptor image inspection proves the recorded repository,
index, manifest and configuration ID, derives the actual supported platform,
and refuses drift without pulling or selecting a replacement image. These unit
checks do not prove a published patched engine or physical lifecycle acceptance.

## Managed ingress logging admission (P09-04)

Pure checks use actual pinned adapted fixtures and refuse implicit default
logging, unsafe error/access/proxy shadow outputs, dynamic logger names, query
filters, extra-field appenders on matching routes, unknown encoder/wrapper/filter
options, credential logging, handler tracing, malformed/duplicate JSON and absent
managed host identity. Route checks cover primary routes, server error routes,
named routes and nested subroute error containers. Unknown server fields,
route-field aliases and null named routes refuse. Namespace
tests cover longest include/exclude precedence, module-wide exclusion, host
casing, wildcard mappings and protected default fallbacks despite access skips.

With `BRINE_TEST_CADDY` selecting local Caddy 2.10.2,
`TestManagedLoggingAdmissionAndLogsWithPinnedCaddy` compares the helper's
decision with actual error/access/debug logs. Synthetic query and bearer
sentinels cover allowed subscriptions, encoded token keys, denied escaped paths,
malformed queries and malformed authority targets. Global and site full-field
deletion and explicit discard must admit without sentinel exposure. Site-only
deletion, query-only filtering, unsafe error/access/reverse-proxy shadows and
extra fields copying the original URI or query token in primary and error routes
must refuse and demonstrate the query leak.
`TestManagedLoggingNamedRouteWithPinnedCaddy` proves an invoked named route's URI
copy also refuses while actual logs expose the synthetic token. Unrelated
primary host-route appenders remain allowed. Bearer header values stay redacted.
All subprocesses and loopback sockets are closed. The helper does not mutate
protected operator configuration. These local probes do not prove native JWT,
WebSocket subscription, physical-host logging or managed deployment admission.

| ID | Scenario | Required evidence | Phase |
| --- | --- | --- | --- |
| T01 | Human and JSON version/doctor output | Valid schema, no ANSI or prompts in machine output | 00 |
| T02 | Invalid config, unknown field, unsafe path/domain/image | Validation refusal; no host or control-plane effects | 01 |
| T03 | Repeated same plan decision facts, including disk movements within one admission class and unrelated app image/health changes | Same canonical hash and ordered changes; sampled perturbations of every included snapshot decision field change hash material | 01 |
| T04 | Changed observed state or policy before apply | Stale plan refused; no mutation | 01/03 |
| T05 | Unknown/unowned service, port or Caddy route | Conflict reported; no overwrite/uninstall | 02 |
| T06 | Rootless fixture restart and full VPS reboot | App resumes with expected status; no Brine process required | 02 |
| T07 | Healthy release | Image digest confirmed; route and direct health pass | 03 |
| T08 | Health check failure/new image never starts | Old release restored or actionable recovery state; no false success | 03 |
| T09 | SSH disconnect or CLI crash mid-operation | Operation persists; status/reconcile identifies actual state | 03 |
| T10 | Two concurrent applies on same target | One wins lock; other cleanly refused | 03 |
| T11 | Caddy reload failure or unexpected config drift | Prior Caddy config preserved; no unrelated site changed | 02/03 |
| T12 | One web app owns a persistent SQLite file | App release replacement and reboot preserve data | 04 |
| T13 | Live data replicated to S3-compatible storage | Isolated restore integrity check + known write verified | 04 |
| T14 | S3-compatible storage outage, missing credentials, expired secret | Deployment/backup health says degraded; no misleading backup success | 04 |
| T15 | Prevent duplicate replicator on same destination | Second replicator refused before data corruption | 04 |
| T16 | Persistent release admission and rollback after schema change | D12 declaration required at plan time; incompatible app rollback conflicts with no changes; automatic compensation refuses an incompatible old writer with recovery-required state; no database rewind | 03/04 |
| T17 | Malicious app name, domain, environment or plan ID | No argv/shell injection, path escape or arbitrary admin command | 00-04 |
| T18 | Agent key attempts an operation outside the allowlist (shell, raw Podman/Caddy, non-Brine software, purge or live restore when policy forbids), invokes the root helper directly, forges an operation record, or substitutes a path/symlink | Dispatcher and root helper each refuse independently of CLI flags and runner-writable state | 06 |
| T19 | Operator performs real S3-compatible storage restore drill to disposable destination | Restore procedure and resulting DB independently verified | 04/06 |
| T20 | Machine/human parity | Same operation ID and status across JSON CLI/TUI | 05 |
| T21 | Limited disk space, killed process, partial write | Planning below the policy disk minimum or with unobserved disk conflicts with no changes; crossing the minimum changes the hash; safe recoverable state and no loss of previous release/config after runtime failures | 03/06 |
| T22 | App removal, recreate, expiry and purge | P03-09 stateless removal withdraws only owned routes/units after writer fencing, retains release/secret history, releases its port, refuses persistent data, and reconciles each effect boundary. P04-08 removal archives under an immutable ID with a restorable backup set for 30 days; remove-recreate-expire never touches the new app; purge only when policy allows; interrupted archive/purge recovers | 03/04/06 |
| T23 | Secrets in logs, plans, TUI or JSON errors | Redaction tests and audit of subprocess output paths | 00-06 |
| T24 | Release binary from fresh environment | Go tests, vet, build and version metadata pass | 06 |
| T25 | Optional WebTransport protocol health, bidi/uni streams and datagrams | P08-01 real local Go echo and HTTP/3-only refusal; named browser evidence for optional-probe/application qualification, not default admission | 08 |
| T26 | UDP spec/policy admission and stale plans | Denied by default; no-effects refusals; hashes bind protocol/bind/exposure/incarnation. Optional TLS binds domain/cert identity | 08 |
| T27 | UDP publish and optional scoped app TLS secrets | Actual rootless mapping matches; no added privilege. Optional TLS delivery also proves exact mounts and cross-app/Caddy denial | 08 |
| T28 | UDP ownership, collisions and drift | TCP/UDP distinction, dual-stack/wildcard overlap and race tests; unknown owners refuse | 08 |
| T29 | Default readiness versus protocol/remote reachability | HTTP health plus exact UDP ownership can be locally ready; protocol/remote facts stay unverified unless probed; opt-in TLS/Origin/settings/blocked-UDP negatives; no network mutation | 08 |
| T30 | Interrupted transport deployment, rollback and removal | Every new effect boundary, SIGKILL, unknown outcome and read-back; no port leak or data rewind | 08 |
| T31 | Optional multi-architecture protocol-health echo image | Published index/platform digests and provenance; real amd64/arm64 runtime evidence when qualifying the opt-in probe, not a default UDP deployment prerequisite | 08 |
| T32 | Shared UDP 443 and ingress authority | Two apps, same-socket multiplexing, migration, idle expiry, SNI/ECH handling; pinned build and whole-config/live proof | 08 |
| T33 | Private WebTransport preview authorization and expiry | Real browser grant/replay/Origin/revocation/incarnation/TTL/isolation cases; public safety gates | 07/08 |
| T34 | Typed data-engine boundary | Existing SQLite plans/receipts/permits stay correct; unknown engines and cross-engine evidence refuse before effects | 09 |
| T35 | Standalone storage, capture and observer | Pinned native snapshot/log private replay, durable offsets, partial active heads, rotation/compaction, metadata checkpoints, marker/schema proof and fail-closed uncertain stop | 09 |
| T36 | Multiarch SpacetimeDB fixture | Published amd64/arm64 app index and platform digests, pinned Wasm artifact, read-only marker checks and browser subscriptions | 09 |
| T37 | Managed service, ingress and identity | App/server permits, rootless resources, public management denial, owner auth, private secret delivery and license-use admission | 09 |
| T38 | Online remote backup and restore test | Existing encrypted tooling against real S3, authenticated snapshot/member checks, isolated replay, protected key recovery, concurrent capture and distinct exact fenced coverage | 09 |
| T39 | Reviewed module create/update | Exact quiesced restore coverage, immutable review/artifact/source binding, staged publish, destructive refusal and schema-aware code rollback | 09 |
| T40 | Engine restore/archive/reconcile | New restore epoch, 30-day retention, remove/recreate/purge safety, interrupted swaps and reboot fences | 09 |
| T41 | Physical SpacetimeDB gate | Actual arm64 Pi Quadlet/Caddy/browser/encrypted S3 drill plus amd64 runtime; online capture while writing, partial heads/metadata drift and fenced mutation/reboot recovery | 09 |
| T42 | Engine operation contracts and runbooks | JSON/CLI goldens, authority refusals, online cadence and exact-preparation downtime/loss/license limits, recovery procedure and SQLite regression evidence | 09 |

Direct-UDP setup, dependencies and acceptance requirements for T26-T30 live in
the [UDP game-server plan](plans/08-udp.md). Optional protocol/TLS qualification
and shared-port/preview requirements live in the [WebTransport extension plan](plans/08-webtransport.md).
Plain UDP delivery has its own exit gate and requires no browser or certificate test.

T34 has local typed-boundary acceptance evidence. T35 has the qualified prerequisite slice below; complete T35-T42 acceptance remains required. Implemented command admission does not establish physical acceptance. Exact dependencies, negative cases and physical requirements live in the [SpacetimeDB plan](plans/09-spacetimedb.md). The gate includes the pinned engine version/image/module digests, online snapshot/log completeness, durable offsets and partial active-head limits, metadata coordination, exact quiesced coverage, protected signing-key recovery and real generic S3 restore. A running server or internal snapshot alone cannot pass.

## Phase 09 operator command and evidence matrix

The [operator runbook](runbooks/spacetimedb.md) describes current public commands.
This matrix maps execution to evidence still required for phase acceptance.
Retain scrubbed commands, exact pins and matching operation receipts. Do not
convert source checks or accepted tasks into physical success claims.

| Task and tests | Public execution | Required acceptance evidence |
| --- | --- | --- |
| P09-04, T37 | Original `plan`/`apply`, `data material set`, post-publication fresh `plan`/`apply`, `status` | Actual rootless service and frontend, app/server permits, HTTPS/WS subscriptions, supplied-key user auth and denied management/publisher routes; startup/reboot fence refusals |
| P09-05, T38 | `backup recovery set`, `backup credentials plan`/`set`, `backup policy plan`/`activate`, `backup capture`, `backup status`, `restore native-test` | Real scoped S3 authenticated repository and complete extraction; signing-key recovery; isolated passive replay equal to confirmed source evidence while writers continue; missed cadence/expiry/outage and interrupted capture/activation resolution |
| P09-06, T39 | `backup prepare`, `data module plan`/`apply`/`inspect`/`resolve`, fresh deployment | Genuine empty first publication with original preparation plan; additive/default preservation and matched clients; destructive/access-change refusals; stale artifact/source/review/role refusals; unknown publish and transition recovery without repeating effects |
| P09-07, T40 | `restore native-live plan`/`apply`/`inspect`/`review-resolution`/`resolve`, `archive native plan`/`apply`/`inspect`/`resolve`, `archive status`, `archive credentials plan`/`set`, `archive maintain plan`/`apply`/`resolve` | Historical data in a new epoch with compatible code, original-tree retention, default-denied live restore, interrupted swaps/reboot fences, full-binding archive/recreate safety, original-scoped credential renewal, thirty-day retention, purge grant/refusals, deadline-bound expiry and unknown deletion recovery |
| P09-08, T41 | The complete runbook on authorized disposable targets | Actual arm64 Pi and amd64 execution, browser and confirmed markers, real S3 online capture/replay, code/module changes, reboot, exact stop/competitor/FDSTORE observer refusals, interruption and archive drills; actual resource measurements and host teardown |
| P09-09, T42 | Runbook and command guides against actual CLI/API | Fresh-agent execution, machine envelope/receipt fields, authority refusals, original-scoped archive maintenance, cadence/maintenance/unknown-loss limits, current artifact pins and shared SQLite regression/physical recovery evidence |

Exact-preparation and workspace cleanup also require an independently qualified
protected-peer observation boundary. A manager or role exemption cannot prove
workspace absence because systemd can retain descriptors through FDSTORE.
The separate protected `/etc/ssh/brine/workspace-observer.toml` grant is
`allow_workspace_observation`, default false. Missing, omitted or false permission
denies observation without changing the deployment operator-policy hash.
The private observer takes an actual owned directory descriptor, never a path,
PID or caller proof. Missing service, busy consumers and unknown observations
preserve the workspace. Source review and actual authorized FDSTORE busy/closed
qualification are distinct gates. The observer source is independently reviewed;
actual authorized FDSTORE and installed-sandbox qualification remains pending.
Fresh enrollment records actual runner identity before listener installation;
requests require completed `enrolled` phase and the explicit grant. Cleanup
records an owned retirement intent after absence proof, adopts a missing directory
only under that exact issued intent, and returns inspection/review cleanup
failures. Required negatives include no-intent disappearance, replaced identity,
missing/false grant, busy FDSTORE and unknown metadata. Keep all related phase
checkboxes unchanged.

## T35 qualified runtime prerequisites, admission pending

[P09 runtime evidence](evidence/p09-runtime.md) records the actual cases and [tools/spacetime-runtime](../tools/spacetime-runtime/README.md) supplies the finite repository gate. The shared Go production primitives are used by the gate; no duplicate schema renderer or mock runtime replaces native replay.

- Canonical v2 tests use real before/restart schema-v10 payloads. Declaration and correctly rebound Typespace permutations preserve evidence; ordered columns/params/index lists, defaults and access changes remain different. Unsupported sections, duplicate names/JSON, recursion, invalid references and resource overflow refuse.
- Fixed private metadata tests verify current environment hash rather than immutable initial_program, exact owner/database/names, module-read bracketing, immutable outputs, no SQL/WS paths, private credentials, scrubbed errors, redirects and byte/deadline limits.
- The repository's native amd64 core gate records 180 explicitly confirmed paired rows, a 135-row closed-only point, and an exact 180-row active-prefix replay. Missing historical replicas can falsely reconstruct initial metadata with bootstrap available; withholding bootstrap in an isolated copy refuses. Source seals and verifier disappearance are checked. Source SIGINT exit 1 is unknown and earns no exact receipt.
- The earlier physical arm64 cases cover torn tails, missing logs/snapshot objects, twelve rotation/finalization captures, mutable completed segments, low-rate tail exposure, metadata-generation drift, ACK-only loss on exit 0, confirmed-read survival, active WS, scheduled writes, SIGTERM/kill/timeout/OOM and rootless UID/entrypoint SIGINT forwarding.
- Supplied-key JWT ingress denies missing/forged/signed-expired credentials and reserved management paths; valid ordinary user WS/exchange passes. External OIDC is unexecuted. Native preflight binding/default backfill passed; AutoMigrate's destructive/access-changing results are not authority.

These prerequisite results do not complete production startup ownership,
quiescence/fence composition or independent exact framing/high-water coverage.
The current managed spec and native dispatcher have implementations; their full
protected Brine/S3 and interruption acceptance belongs to T37-T41 and the
physical exit drill above.

## T39 schema/publication core, admission pending

[Core evidence](evidence/p09-schema-publication.md) retains six actual final native descriptor payloads and their artifact/schema/content pins. Tests cover strict V10 envelopes and shared budgets, preserved declaration order, actual default bytes, access/removal/default/constraint changes, explicit names on existing tables/functions/indexes, required effect reviews and immutable returned evidence. Same-schema code updates still require guest review and data verification.

Pure publication tests cover disjoint recovered-empty versus captured-history records, supported source artifact/schema pairs, derived projection requirements, requester/source/expiry freshness, app-bound distinct secret roles with custom key names, private codec refusal of extra/null/duplicate/mixed fields and immutable review/proof slices. Cursor tests cover unknown-effect inspection, no repeat after matching observation or known nonperformance, delayed creation identity adoption, every required verification proof, verifier disposal and an external transition receipt. Coherent journal truncation cannot reopen publication when decoded against the independently retained record hash. Malformed-record tests supply matching hashes to exercise shape/state refusal separately from the retention check.

These historical core tests are metadata/domain regressions. Subsequent
implementation connects protected extraction, journal/claim and role lookups,
exact remote preparation, staged publication/data projections, bundle swap,
new epoch and jobs/dispatch. Their physical T41 acceptance remains open.
Neither P09-06 nor T39 is completed by this source unit.

## T16 schema policy acceptance, physical completion pending

D12 and the [Schema migrations contract](CONTRACTS.md#schema-migrations) define these gates. P04-07's policy documents alone are not passing behavioral evidence. Connected declaration/current-schema admission, fresh writer permits and reviewed first initialization now exist, but existing-schema migration and the physical incompatible-rollback drill remain open.

- Persistent create and update plans without complete per-database declarations conflict with `schema_compatibility_required` and have no executable changes. Config/start paths cannot bypass admission. Removing a data reference cannot disguise existing persistence. Strict parsing refuses unsupported startup migration modes. Existing stateless plans remain unchanged.
- Known schema incompatibility conflicts with `schema_incompatible`. Unknown or contradictory current schema conflicts with `schema_state_unknown`. Declaration ordering preserves canonical hashes. Changed current schema invalidates a stored plan at apply before effects. Initial table creation uses the separate reviewed schema-change workflow.
- Rollback planning checks the target release against current database markers, not historical release markers. Compatible rollback preserves fixture writes and schema. An incompatible target conflicts with `schema_rollback_incompatible` and has no effects. A failed candidate, reconcile and resolve cannot restart incompatible old code or reuse stale `compatibility_verified` proof. They record `recovery_required`, never a successful rollback or automatic database restore.
- Later migration automation refuses missing review, denied policy and absent, expired, mismatched or unavailable verified restore points. Every affected database needs coverage of quiesced pre-migration data. Interrupt each schema-effect and journal boundary. Inspect partial or unknown outcomes without replaying SQL, a down script or live restore.
- A separately authorized physical drill records an app fixture's schema and known writes, applies a reviewed schema change, and proves incompatible app rollback refuses without changing either. A real isolated S3-compatible storage restore verifies the exact recorded point, integrity, invariants and schema. The recorded Pi drill now proves isolated S3-compatible recovery, but it did not perform the reviewed schema change or incompatible rollback required by this case.

## Phase 04 foundation review acceptance, partial implementation and physical evidence

- T12/T15/T22 exercise fail-closed permits for both application and replica starts, including default-target boot, automatic restart and credential replacement. Reboot/interrupt after replica stop, archive rename, DB swap and new-epoch activation. A held fence or missing/unreadable/inconsistent permit state blocks both; reconciliation settles bundle/config/epoch before safe resume, and live restore never activates its old epoch. Host-local locks are not cross-host exclusion; foreign/ambiguous destination ownership refuses.
- T12/T15: admitted cadence changes preserve app units/routes and replica identity/epoch; unchanged cadence has no effects. Interrupt immutable publication, stopped-unit replacement before binding commit, committed binding before restart, and unknown stop/start attempts. Read back the durable cursor before resume and never reissue an uncertain stop/start. A held fence stores a pending revision only, then activates after release. Credential rotation continues through the same replica restart cursor; its existing tests remain unchanged. Unit/fixture coverage does not replace the physical SQLite/reboot/destination-exclusion drill.

- T13 verifies atomic create-only capability with competing conditional uploads; exactly one wins and its bytes remain unchanged. Unsupported capability refuses preparation, with no HEAD-then-PUT fallback. Exact LTX receipts bind a successful private `sync -wait` upload barrier and selected `-txid` to an isolated restore. Local `litestream status` and timestamps do not prove remote transaction recovery. Restore checks require integrity, zero foreign-key violations, schema and declared invariants.
- T14/T16 require strict pinned-model config validation, including typo/duplicate/unknown keys and `l0-retention: 0`. Schema observation covers absent and nonwritable SHM plus concurrent WAL commits using logical read-only access, never `immutable=1`. Normative JSON catalog golden vectors in the persistence contract cover escaping, NULL, Unicode, ordering and the reserved empty marker.
- The coordinator's Debian 13 arm64/Podman 5.4 shell probe establishes keep-id ownership and five-write local replication/restore only. It does not complete protected enrollment installation, generated-unit, reboot/interruption or real S3-compatible recovery gates.

## Phase 04 acceptance audit, 2026-10-11

Audited main at `f6e9976` against code/tests and the recorded physical/storage drills. See [scrubbed evidence and source provenance](evidence/p04-pi.md). A checked row covers its stated scenario only, not every additional foundation gate or the whole phase. The required evidence text in the table above is unchanged.

| Row | Acceptance | Evidence and remaining clauses |
| --- | --- | --- |
| T12 | [x] | Real environment-release replacement preserved rows; ordinary reboot returned a healthy app with the latest marker intact. The new release used the same image. Distinct-image replacement is not claimed. Additional adverse permit/archive/swap/new-epoch foundation cases below remain open. |
| T13 | [x] | Real S3-compatible isolated LTX and snapshot restores passed integrity/FK/schema/invariants and independently recovered a known sequence/marker/commit-time row. The connected Pi job proved production binding/config and fixed observer. The additional competing conditional-upload and complete live-data preparation gates remain open under P04-04. |
| T14 | [ ] | Admission tests refuse absent/expired credentials, stale or expiring retention proof and invalid pinned config. Real access/rotation passed. **Remaining:** physical storage outage, missing credentials and actual expiry must report degraded deployment/backup health without false success. |
| T15 | [ ] | Store uniqueness, actual local lifetime flock and foreign/fenced permit tests exist. Real normal rotation used independent stopped-service/lifetime-lock checks. **Remaining:** physically start a competing/held-lock/foreign-owner replicator and prove refusal before corruption. |
| T16 | [ ] | Required declarations/current-schema/rollback conflicts, fresh compatibility permits and compatible physical compensation exist. **Remaining:** reviewed schema change and incompatible manual rollback/automatic compensation, reconcile/resolve and unchanged schema/data proof on the physical fixture; existing-schema migration plan and exact pre-migration restore coverage. |
| T17 (Phase 04 slice) | [ ] | Strict typed data paths, config, credentials, restore sources and dispatcher refusal tests cover synthetic hostile inputs. **Remaining:** complete the shared malicious-input acceptance against the restricted physical boundary; the normal deployment/restore drill is not a shell/path/admin escape test. Later-phase T18 authority is not waived. |
| T19 | [x] | Operator-driven real storage restore and connected Pi restore used disposable private outputs; independent DB inspection proved exact sentinel contents, integrity/FK checks and separate paths/inodes. Live serving remained intact. |
| T22 (persistent slice) | [ ] | Initialized removal refuses rather than delete data. Local prepared-only, held-fence, settled not-initialized and initialized-without-release regressions now refuse with archive required, preserve journals/fences/bindings/restore receipts, and do not save a misleading no-op. Final physical stateless regression removal preserved unrelated persistent heads. **Remaining:** archive IDs and fresh remote coverage, 30-day restorable retention, remove/recreate/expire isolation, policy-gated purge and all archive/purge interruptions, including prepared-incarnation retirement. |
| T23 (Phase 04 slice) | [ ] | Credential redaction and bounded subprocess-output tests exist; public evidence is scrubbed and credential values are not copied into it. **Remaining:** complete the full cross-surface logs/plans/TUI/JSON audit. This CLI/storage drill does not prove TUI behavior. |

Additional foundation acceptance remains open even where a narrower row is checked:

- T12/T15/T22 still need physical reboot/interrupt after replica stop, archive rename, DB swap and new-epoch activation; missing/unreadable/inconsistent permits and old-epoch exclusion. An injected committed fence and an operation-owned uncommitted initialization fence passed startup probes. Neither is every live-restore boundary.
- T12/T15 cadence-only replacement and credential rotation passed physically without an app restart. Unit/local-store tests interrupt publication, binding commit and uncertain starts/stops. The physical drill did not interrupt each cursor boundary or challenge it with a competing lock owner.
- T13's real gate passed conditional snapshot uploads, exact private sync barrier, LTX selection, isolated restore and schema/invariants. Pi empty initialization passed the production sequential create/second-create-refusal check. A real competing conditional-upload race and full journaled live-data preparation remain unverified.
- T14/T16 strict config/catalog and logical-read-only WAL observer tests are code evidence. Physical adverse storage/credential/schema-change cases are not supplied by those tests.

The Phase 04 exit-gate evidence set is not complete. No mock or running replica is substituted for a remote restore, degraded-health drill or physical exclusion proof.

## Required gate before real personal data

Record **the exact host distribution, Podman/Caddy/systemd/Litestream versions**, release commit and image digest; verify T05-T16, T18-T19, and T21-T23 on an authorized test target. Run at least one power/reboot-style recovery exercise and one restore from actual S3-compatible storage; simulated success is insufficient.

Document remaining limits prominently: a single server is not high availability; accepted downtime is not zero downtime; Litestream is asynchronous, not a guarantee of no data loss; an agent with direct SSH/sudo/raw runtime privileges can bypass policy.

## Repeatability

Each integration scenario must have a fixture, setup, expected behavior, teardown limited to **resources created by that fixture**, and captured redacted evidence. Never run a blanket `podman system prune`, delete unrelated volumes, flush firewall state or remove another app as part of test cleanup.


## P03 lifecycle and preflight deadline evidence

- T04/T08/T09: preflight deadlines report `inventory_failed`, preserve the
  deadline cause and fail before mutation, including facts returned after expiry.
- Unknown-stop tests still require a durable unknown outcome, successful settlement
  or recovery-required for a pending job. Short pending-job budgets begin only
  after the uncertain action, not during unrelated preflight reads.
- T08/T09/T21: direct typed unknown install and restore outcomes use the default
  facts reader after the durable unknown event, with a fresh bounded context and
  exactly one mutation call. A real-directory install failure before predecessor
  retention proves unchanged unit bytes and both previous-release health checks.
  The rig rejects missing, mismatched or completed inspection outcomes and
  unbounded reads, and still rejects mutations without their matching intent.
  Filesystem regression tests use the production effect budget; injected unknown
  outcomes exercise inspection without waiting for a deadline.
- These are local fake-adapter and temporary-directory tests, not physical-host
  lifecycle evidence.

## P03-09 local evidence (physical host pending)

- T04/T05/T17/T18: removal planner ownership and persistent-unit tests; stale
  connected apply journals `stale_plan` with no effects; hash, marker, symlink
  and mounted-data deletion refusals; mutating dispatch authorization and strict
  app-only argument tests refuse paths, arbitrary units and purge flags.
- T09/T10/T21: `internal/host/remove_crash_linux_test.go` SIGKILLs an actual
  run-op subprocess after each of five durable fake effects, proves it held the
  host lock, verifies dry-run reconciliation changes neither journal nor fake
  host, and converges through a detached recovery receipt. Eighteen unit prefix
  cases cover before/after/journaled boundaries; drift and unknown writers/jobs
  fail closed. Unknown-effect tests journal/read back without blind retries.
- T11: complete Caddy generation settlement tests refuse foreign hash drift and
  adapted routes that still send traffic to the removed application; disk state
  alone is never treated as evidence that an uncertain reload succeeded.
- T20/T22/T23: human/JSON/JSONL removal goldens; connected removal and repeated
  no-op; immutable retirement receipt, retained release history, freed port and
  old-removal/new-head protection. Machine/human output explains D5 retention;
  the executor never calls secret/data deletion adapters.
- Gate: Go tests/race tests, Linux and Darwin arm64 vet, build, gofmt and diff
  checks. Local fakes and SIGKILL are not physical Caddy/Podman/systemd, SSH
  disconnect or reboot evidence. P04-08 archival/restore/expiry/purge and the
  separately authorized physical-host acceptance lane remain pending.

- Review regressions (failing-first): removal after registry/domain revocation,
  including an unrelated revoked app; interrupted no-op completion after
  intent/completed preflight and refusal of changed/unknown absence; spaced/tabbed
  Volume/Mount/ReadWritePaths directives; unified v1-to-v2 migration preserving
  the deploy journal and creating immutable removal receipts. All pass locally.

### Terminal recovery resolution regressions

- T09/T10/T21: exact terminal removal prefix (withdraw completed, stop unknown/
  interrupted) with stopped writer and withdrawn route; dispatcher/store/runner
  resolution retires head/port without replaying withdrawal or stop. An actual
  v2 database migrates to v3 preserving that journal and terminal identity.
- T07/T09: equivalent first-deploy and update installed-candidate readback
  rolls back safely; ambiguous live owned-unit hashes refuse with no effects.
- T09/T10/T21: local run-op SIGKILL at remaining removal deletion, reload and
  retirement effects; read-only preview, host-lock proof, detached reconciliation
  and repeated convergence. This is fake-host evidence, not Pi execution.
- T20/T22: closed resolve CLI/dispatcher protocol, mutating D8 denial and
  acceptance, idempotent creation, active-successor exclusion and immutable
  terminal/source guards. Secret present/absent resolution and interrupted
  successor reconciliation inspect only the named version and never create it.
- Historical-artifact regression: terminal removal with a committed pre-log-policy
  Quadlet hash succeeds after the renderer adds LogDriver/LogOpt. Inspection and
  deletion use the recorded hash, not freshly rendered unit bytes; normal drift
  checks remain enabled. Both new diagnostic rules survive the main merge.

Resolution receipt ancestry: committed/retired evidence held by the original,
intermediate or immediate source is retained across repeated terminal resolution
attempts. Foreign plan/app, non-recovery-required state, unsupported kind,
cyclic and overlong (more than 64 ancestors) chains refuse without host effects.
## P03 imported-route provenance evidence (physical rerun pending)

- T05/T11: `internal/inventory/provenance_test.go` uses the production collector,
  generation filesystem shape and real Caddy v2.6.2 adapter JSON. The source root
  has a global `local_certs` block plus the fixed generation import. Fixtures
  cover an empty generation, one app, a separate operator site, an overlapping
  HTTP site and a catchall. Capture used `caddy adapt --config Caddyfile
  --adapter caddyfile`, with the fixed import redirected only to a local fixture
  generation. Adapter JSON contains only reserved example names and loopback
  upstreams; capture paths and diagnostics are not included.
- A create plan followed by a committed-release environment-update plan passes
  using collected routing facts, not pre-attributed routing snapshots. Before
  the fix, the update test reports `artifact_drift caddy.live`, `domain_owned
  domains` and `unknown_facts live_caddy_files.domains`.
- Root-owned domains, overlapping owners and catchalls still refuse with
  `domain_owned`. Changed rendered bytes, failed or unmatched adaptation,
  unknown generation and disk/live disagreement never authorize an update.
  Complete disk-adapted/live JSON equality remains a prerequisite.
- These are local collector/planner and real-adapter fixtures, not a successful
  Pi update, rollback, removal, recovery or S3-compatible storage restore. Physical reruns belong
  to the separately authorized host lane.

- Review regression: real Caddy v2.6.2 adapts an unused snippet containing the
  generation import plus an equivalent root-owned site to the same JSON as a
  real imported site. The failing-first collector/planner test proves route
  equality alone cannot establish import execution. Reusing D4's root lexer
  now leaves provenance unknown and refuses the update. Source-context tests
  also refuse imports in site blocks, named routes, other files and roots with
  additional imports.

- Diagnostic consumer regression: `internal/diagnose/collector_provenance_test.go`
  feeds the production collector directly into the diagnostic reader. Healthy
  attributed files now prove the live generation and route presence. Every
  selected file must still have known live evidence; missing sources, wrong app
  attribution or unknown domains keep `live_generation_unproven`. Opaque names
  remain valid only for currently emitted unattributed sources, not as a
  substitute for an app association on filename-shaped sources. The diagnostic
  change is confined to `liveGeneration`; log collection paths are unchanged.

- The imported-file boundary uses the same pinned Caddy lexer, including quoted
  import directive names. A real-adapter probe confirmed that a quoted import
  in another unused snippet contributes no routes while the executed app import
  still produces equivalent JSON. Its failing-first collector regression now
  refuses all attribution. Lexer tests distinguish quoted directives from
  harmless response strings and comments; environment expansion and malformed
  source tokens also stay unknown.


Resolution review regressions (local, physical rerun still pending):
- Completed check_direct on an interrupted update with foreign or unreadable
  Quadlet ownership refuses before rollback_quiesce can stop its service.
- Both intent and completed health boundaries freshly probe pending manager
  jobs; a continuously pending job refuses without host effects.
- Durable phase transitions before step intent converge at every removal
  boundary. Actual detached run-op SIGKILL after each of the six forward phase
  writes proves read-only preview, successful settlement and repeat convergence.
- Transactional family fencing rejects a second resolution of an old source
  while a descendant is active or after that descendant succeeded.
- Status suppresses resolve advice for absent and unsupported prefixes; diagnose
  directs the reader to status rather than promising automatic resolution.

- Post-intent effect races: fresh foreign-file, pending-job and unreadable-job
  changes refuse at rollback quiesce, unit restore and previous start, for both
  terminal resolution and ordinary deploy rollback (18 cases). The same races
  refuse normal forward quiesce, install and start (9 cases). Removing the
  shared guard reproduces failures; with it all 27 cases pass repeatedly.
- A disappearing first-deploy candidate refuses rollback stop even though
  absence will later be an allowed result of safe unit removal.
- A successful rollback stop with a queued manager job cannot restore a unit
  or start its predecessor. Pre-effect refusals never become unknown/applied
  outcomes. Real pinned-root filesystem tests prove owned hash/explicit absence,
  fresh replacement detection, marker and symlink refusal, and cancellation.

- Changed policy version or hash, while the app remains allowed, refuses with
  zero mutation calls after predecessor quiesce and after candidate installation
  (four update-resolution cases).
- A queued job appearing after intent records an explicit no-effect refusal;
  after it clears, both inspection paths can authorize recovery. Ordinary deploy
  rollback refusals at quiesce, restore, reload and predecessor start are
  resolvable, including first-deploy cleanup after the candidate was removed;
  completed rollback effects are not replayed (seven boundary cases). Unknown,
  failed, out-of-order restore evidence and missing compatibility proof refuse.
- Refusal projection tests reject absent/mismatched intents and invalid proof
  outcomes/steps, preserve original payloads and never infer no-effect proof
  from generic rollback failures.
- Deploy, removal and resolution SIGKILL tests keep child/barrier startup
  bounded separately from parent recovery and assertions. Deploy recovery uses
  a fresh bounded observer after its detached worker finishes. Earlier contexts
  are canceled before observation, so accidental reuse fails without sleeps.
  Production timeouts and the resumed-health deadline assertion are unchanged.

### Stop status and host-port isolation

- T09/T12: rendered stateless and persistent Quadlets accept numeric SIGTERM exit 143 only with a forced restart for spontaneous 143. Explicit stop must leave inactive/dead with no pending manager job or container; exit 1 or 137 remains a failure and repeated spontaneous 143 reaches the start limit. Persistent restarts repeat writer-permit and writer-attempt; held fences block them.
- T05/T09: another committed app with failed or unknown runtime state retains its stable port without making new-app admission unknown. Reserve both committed and differing observed ports, including missing runtime app records. Unknown ports without a committed app record still refuse. The failed app's own plan stays conservative.
- T12/T15: stop the independent pinned Litestream replica and check inactive/dead and exit zero. Shutdown errors remain failed. Starting or restarting it under a held fence must fail replica-permit, and pending jobs must settle before quiescence is accepted.

Rendering and pure planner tests cover the policy and reservations locally. The physical systemd/Podman stop, crash and fence drill remains required.

### Image inventory and plan freshness regression

- T03/T04/T05/T22: running image inventory proves both single-manifest pins and locally retained index/selected-manifest aliases without a registry read. A short planning budget and a longer detached-worker budget produce the same deploy, stop and remove hashes immediately after deployment. Missing index association remains unknown without any registry request, even when an exact manifest list is available. Running-container and stored-image alias-layout regressions assert no manifest command runs. Stalled local probes still receive fair bounded slices across apps.
- T03/T04: unrelated apps' known, unknown, absent or unsupported image observations and failed unit activity do not change another app's plan hash. The planned app's image status/digest and owned unit changes still change its hash. Removal still refuses owned unit drift. Executor tests also require a target image digest/status change to emit plan drift before any removal effects.
- These are fake collector and pure-planner checks. Physical acceptance requires fresh connected plans and one-shot apply on an authorized disposable fixture; no physical check is claimed here.

## P10-04 combined first-deployment acceptance

Local CLI orchestration fixtures exercise T09/T10/T14/T16/T23 without a real
host: ordered independent preparation/credential/initialization/release jobs;
preaccept and settled-terminal failures at every stage; lost acceptance and
lost polling responses; requester-bound read-only job recovery without secret
redelivery; valid foreign/same-app-different-spec checkpoint plan refusal;
release decision changes after initialization requiring a new review; unknown initialization observations before task settlement;
recovery-required refusal; private locked resume files; multiple databases;
already-initialized and stateless releases; changed later releases; review-only,
credential file admission and guarded/cancelable human confirmation.

Tests live in `internal/cli/deploy_test.go`,
`internal/cli/deploy_resume_test.go`, `internal/dispatch/deploy_test.go` and
`internal/host/deploy_linux_test.go`. These are local synthetic/adapter tests,
not physical deployment, replication or R2 restore evidence. Recorded physical
first-app and completed-stage client-kill evidence is audited below. P10-06
remains incomplete; local tests do not fill its final-release interruption gap.

## Phase 10 acceptance audit, 2026-10-11

Audited `b0a7de7` against implementation/tests and every recorded Phase 10 JSON
receipt, with follow-up full recreation receipts from installed `1692095` (#110). See [scrubbed evidence and source provenance](evidence/p10-pi.md).
The required evidence in the shared scenario table is unchanged. A checked
slice below is not completion of the entire row or of additional foundation gates.

| Row or gate | Acceptance | Evidence and remaining clauses |
| --- | --- | --- |
| P10-01 | [x] | One/several database env goldens, collision/reserved-key refusals and mount-path hash changes have local tests. Actual Pi container process environment contains the engine/path/accepted marker variables; the app opens the injected path. |
| P10-02 | [x] | Shared client/host catalog goldens cover all four DDL kinds. Source-line refusals and whitespace/comment versus schema-change marker tests cover the stated deterministic acceptance. Physical initialization uses repository SQL. |
| P10-03 | [x] | Tampered bytes refuse before publication; fenced immutable same-digest storage and same-plan repeat/settlement have domain/store/host tests. Physical first initialization and isolated restore passed. The original recreated-app inventory refusal was fixed by #110; follow-up physical initialization/release and exact new-sentinel restore passed. |
| P10-04 | [x] | Ordered happy path, preaccept/settled failures at all four steps, nonreplay continuation, initialized-app path and JSON IDs have local tests. Pi evidence adds one combined SQL review, healthy first app, three completed-stage client-kill resumes and initialized update. No final-release physical kill is claimed. |
| P10-05 | [x] | Single-page contract/spec/schema/driver examples and add-column/custom-build guidance cover the requirements. Coordinator clarification on 2026-10-11 replaces the ten-line size hint with "short, complete examples"; imports/error handling are retained. Rust/better-sqlite3 execution remains unverified and is not an additional documentation acceptance requirement. |
| P10-06 | [ ] | Physical env/init/replication/exact-sentinel restore and three completed-stage client-kill resumes passed. **Remaining:** physical final-release interruption and resume, required by "at each step." Owner decision on 2026-10-11 drops the alternate-version/compile-options check. Full archive recreation now passes on #110, but belongs to T22, not P10-06 acceptance. Every internal detached-effect boundary is not an additional P10-06 gate. |
| T04 (in-range listener slice) | [x] | Real TCP and UDP listeners inserted after plan acceptance each caused `stale_plan` before task effects; release head and route/unit bytes remained unchanged. Injected listeners were removed and ports verified free. This is not every possible observed-state/policy drift case. |
| T07 (Phase 10 slice) | [ ] | Real healthy first release and final initialized update passed; connected status records the current image digest and healthy direct probe. **Remaining:** an explicit passing route-health receipt for the complete row. Healthy direct response alone does not prove both paths. |
| T09 (Phase 10 client checkpoint slice) | [x] | SIGKILL after completed preparation, credentials and initialization retained accepted jobs. Final reruns completed and reused completed references. A naturally lost recreation-preparation acceptance reply was inspected to a succeeded host receipt without blind resubmission. This does not prove every in-flight remote crash boundary. |
| T10 | [ ] | Private locked resume state and host serialization have local fixtures. **Remaining:** physical concurrent same-target applies with one winner and clean refusal; the sequential interruption drill is not concurrency evidence. |
| T13 | [x] | Another real selected-epoch remote restore recovered the exact sequence/marker/commit-time sentinel with passed integrity/FK and verified schema. The receipt's declared-invariant check/loss window remain unknown. Additional conditional-upload/foundation gates are not completed here. |
| T14 | [ ] | Externally supplied credentials and real remote access succeeded. **Remaining:** physical outage, absent credentials and actual expiry with truthful degraded-health reporting. Local refusals and working access are not adverse-provider drills. |
| T16 | [ ] | Preserve-only first initialization and already-initialized release paths passed. **Remaining:** reviewed existing-schema migration and incompatible rollback/compensation/reconcile/resolve without data rewind. No schema-changing physical drill is supplied. |
| T19 | [x] | Connected real remote restore into an isolated disposable file plus independent exact-sentinel SQLite inspection; live data untouched. No live restore claim. |
| T22 (persistent slice) | [ ] | Physical archive retire preserved inode/sentinel and old fences; abandon preserved source and allowed normal start; full recreation on #110 reused preparation/credentials, initialized/released a healthy fresh incarnation, grew its fresh epoch and independently restored the exact new sentinel; old archive sentinel/disposition/deadline stayed unchanged. **Remaining:** thirty-day restorable retention/renewal, expiry, allowed/denied purge and every interrupted archive/purge boundary. A recorded deadline is not elapsed retention proof. |
| T23 | [ ] | Local credential-redaction fixtures and scrubbed public evidence exist; acceptance-reply recovery did not blindly redeliver the secret. **Remaining:** full logs/plans/TUI/JSON-error and subprocess-output audit. This CLI drill does not prove TUI behavior. |

No new Phase 04 full-row checkboxes are earned by the limited archive exercise.
The [earlier Phase 04 audit](evidence/p04-pi.md) remains a dated account; T22's
new partial evidence above does not replace its still-open retention/purge gates.

### P04-08 archive preparation evidence (a1)

**Status: Approved; full T22 remains open.** Local tests cover immutable archive IDs and manifests; device/inode and ID-derived path/epoch binding; initialized versus never-written evidence; append-only journal transitions/CAS and durable fences; unknown/unowned systemd refusal; independent live Podman mount/process refusal; create-only snapshot upload and independent fake-storage restore of initialized rows with source-bound coverage; and refusal to replay preparation interrupted at each journal/effect boundary. Unsupported initialized multi-epoch coverage refuses during planning and fresh pre-claim admission, with no plan saved, fences claimed or writers stopped; never-written multi-epoch planning remains supported. Preparation is not removal and does not exercise rename, retirement, retention, recreation, expiry or purge.

Physical verification is reserved for the separately authorized disposable-host drill lane. On that fixture only, record `brine host archive-prepare APP --plan` under the enrolled identity, inspect the resulting ownership/path/epoch bindings, then run `brine host archive-prepare APP --plan-id PLAN_ID`. Verify the independent restored last committed row, cutoff and exact source receipt, stopped writer/replica process/cgroup/job observations, lifetime-lock exclusion, held startup fences, immutable manifest and unchanged live source tree. Repeat the same plan and verify no upload or stop effects replay. On separate fresh fixture incarnations, interrupt after each of `intent`, `writers_stop_intent`, `writers_stopped`, `coverage_intent`, `coverage_verified`, `replica_stop_intent` and `quiesced`; repeat preparation and require `recovery_required`, unchanged ownership and held fences. Do not clear fences or manually retire reservations as a substitute for the forthcoming recovery operation. No Pi or real provider action was performed by the implementation lane. Full remove/recreate/expire and T22 rename/purge interruption drills remain required after a2/a3/b.


### P04-08 archive retirement and abandonment evidence (a2)

**Status: Approved; full T22 remains open.** Local filesystem tests prove no-replacement rename, symlink-parent refusal and retained inode identity, including deterministic source-parent, destination-parent and simultaneous symlink swaps at the final syscall boundary without moving outside data. Actual rendered persistent Quadlet tests prove archive cleanup accepts owned `Volume=` units while stateless removal still refuses them and database bytes remain unchanged. Domain/store tests cover journal boundaries, refusal to replay interrupted cleanup/rename/commit, stale-coverage retirement refusal, safe abandonment despite stale contextual evidence, exact owned-fence release, preserved manifests/history, schema migration, release-head retirement, inventory absence and fresh incarnation/database/binding/epoch/path/prefix reservation on recreation. Domain and real SQLite tests cover every pre-manifest stage, interrupted abandonment adoption, exact own-fence-only release with an unrelated held fence unchanged, refusal before cleanup intent when prerequisites fail, and a safe exit after an affirmative pristine pre-effect cleanup failure. Abandonment restores ordinary startup admission, not automatic service launch.

On the separately authorized fixture, first prepare a fresh archive, then run `brine host archive-abandon ARCHIVE_ID`. Verify unchanged source bytes/identities, journaled fence release and successful ordinary start/apply; prepare again and verify new archive/operation IDs. On another fresh fixture, run `brine host archive-retire ARCHIVE_ID`, verify owned route/unit removal, no live mounts/processes on source or archive paths, unchanged archived inode/database bytes, atomic active retirement and the thirty-day receipt clock. Recreate and verify every identity/prefix is new and old held fences cannot affect it. Interrupt every preparation stage before manifest publication and the abandonment intent; require verified safe abandonment. Interrupt cleanup before effects and verify an exit only while all owned artifacts/routes/source identities remain pristine. Interrupt actual cleanup and each rename/commit boundary on separate fixtures and require `recovery_required` without replay or abandonment. These are drill instructions, not executed evidence. Inspection/adoption, ongoing retention/renewal, expiry and local-only purge remain a3/b work; keep T22 unchecked. No real host or provider action was performed.


### P04-08 interruption inspection/adoption evidence (a3)

**Status: Approved; P04-08 and full T22 remain unchecked.** Local domain/filesystem regressions interrupt cleanup completion, rename intent, rename receipt and retirement commit; inspect without changing the journal; and reconcile the exact recorded inode to terminal retirement without repeating cleanup or clearing fences. Multi-root tests adopt one moved tree while the next remains at source, and refuse an unjournaled later movement. Unknown runtime ownership, active writers/replicators, missing paths, duplicate source/archive paths and replaced inodes refuse without advancing the journal. Independent manager tests cover loaded-stopped and affirmatively absent units, jobs/cgroups, and live Podman mounts at both source and archive paths without stop calls. Quadlet inspection distinguishes absent, present-owned and changed artifacts without mutating them. Archived schema/emptiness and parent fsync use recorded ID-derived allocation/tree identity. Recovery re-verifies initialized remote coverage instead of extending the old receipt's freshness; current authorization and retention must pass.

On the separately authorized disposable fixture only, interrupt after cleanup intent, route withdrawal, writer-unit removal, each replica-unit removal, daemon reload, runtime receipt, each rename intent, rename syscall/fsync and rename receipt, and immediately before/after retirement transaction commit. Run `brine host archive-reconcile ARCHIVE_ID --inspect`, record unchanged journal/artifacts and observed inode locations, then reconcile that ID. Require terminal retirement, held old-incarnation fences, no start/stop effects, identical database bytes and a freshly independently restored exact original remote point for initialized data. Re-run after recreation and require only the retained historical receipt, with the new incarnation untouched. Inject unknown process/manager evidence, changed artifacts and missing/conflicting/replaced/out-of-order paths; require recovery refusal and unchanged ownership. These are drill instructions, not executed evidence. Ongoing 30-day restorable retention, archive credential renewal, expiry and local-only purge remain b work. No Pi, physical host or real provider action was performed here.

### P04-08 prepared-root admission regression

**Status: Approved; P04-08/T22 remain unchecked.** Local regression uses real `ProbeRoot`, persisted preparation evidence and real `InspectRoot` through archive facts, checks that planning creates no probe artifacts, and refuses a replaced root. Shared evidence composition tests refuse missing/incomplete probes and changed root/device/inode/filesystem while retaining current capacity and observation time. A bounded live-WAL regression exercises archive candidate reads through independent writable and read-only store handles while another handle records writer attempts. This does not claim the Pi's transient concurrent-initialization error was reproduced or resolved; the read-only URI uses ordinary `mode=ro` with WAL shared-memory access, not `immutable=1` or a copied database. Physical archive/retention/purge acceptance remains with the authorized drill lane.

### P04-08 retained maintenance primitive regressions

Local unit coverage exercises the exact expiry boundary and unchanged thirty-day deadline; refusal of unreviewed purge and stale ownership; failed initial journal claims without inferred recovery authority; deletion-intent, unknown-after-effect, receipt and completion interruptions; terminal no-effect reconciliation; and failed retention checks preserving old proof timestamps. Ordered multi-root tests stop after an unknown first-root deletion, preserve the later root until its own intent, reconcile in order, and refuse out-of-order later-root absence. Initialized proof validation separately checks fresh exact source/schema/cutoff, mandatory receipt and invariant evidence, and immutable validity limits.

Real temporary filesystem tests cover exact-bundle-only deletion, explicit absence adoption, a replacement archives parent hiding the original bundle, preflight rejection of symlinks/hard links/public modes/FIFOs without deleting good files, canceled deletion and resuming an explicitly simulated partial local deletion. Store tests exercise immutable reviewed plans/proofs/deletion intents across close/reopen, stale compare-and-swap refusal and early-expiry/deadline-change refusal.

Archive credential tests exercise distinct archive/binding/epoch namespaces, unchanged retention clocks and ordinary fenced-scope refusal; metadata-only receipts without activation; exact sealed unknown-delivery adoption; changed-authorization refusal; partial version reservation; private packet handling; and coexistence with the existing ordinary credential-delivery tests. Store-level tests additionally verify original scope across recreation/reopen and preservation of held fences. These are synthetic credentials and local files, not evidence of successful provider access or a thirty-day remote guarantee.

Connected host tests use real prepared allocations, retirement journals and archive renames with synthetic manager/container observations. They admit untouched retained roots, ignore a running unrelated recreated incarnation, refuse target drift and original replica PID/job/cgroup or source/archive mount activity, and prove original lifetime-lock exclusion outside archived trees. Exact archive credential delivery keeps fences held, reports sealed metadata only, selects the original scope and refuses a missing environment. An isolated restore-engine test downloads and independently checks an original empty SQLite snapshot from a signed local fake provider, refuses another binding/epoch and insufficient credential lifetime, and leaves archived bytes untouched. This exercises initialized remote-source machinery, not a successful initialized production archive or provider guarantee.

Machine regressions reject paths/unknown kinds/ambiguous reviewed plans/invalid expiry before runtime setup, bind apply/delivery to explicit IDs, select process safeguards and avoid credential output. Status preserves prior verification timestamps through stale/degraded, expired and locally deleted states. Descriptor tests also preflight depth overflow and set-ID/sticky entry modes.

Before T22 is checked, complete the separately authorized physical fixture: initialized original-source retention with real provider access, issuer credential/lifecycle expiry and renewal, multi-root interrupted purge, and protected namespace/process races. Full queued local gates and exact-head CI remain delivery requirements. No remote object deletion is part of the test or implementation surface.

Retained purge mount-boundary regressions compare known/missing/zero/different mount-ID evidence without privileges. A separate subprocess with its own private mount namespace bind-mounts live data into an archived bundle and checks both live bytes and an ordinary archived file survive refusal. The mount regression explicitly skips where namespace or mount permissions are unavailable; such a skip is not physical mount-isolation evidence.

Retained multi-root reconciliation adopts an already missing first tree only under its pre-existing in-flight intent, then refuses a later tree renamed away immediately before its deletion call. The regression asserts recovery-required at the later intent, no deletion receipt/completion, and preserved moved-tree bytes.


## Native capture structural prerequisites (P09-05 / T35)

`go test -race -count=1 ./internal/nativecapture` checks immutable inventory,
checkpoint, and source records. Cases cover the following structural boundaries:

- A snapshot inside one log segment, a directly adjacent retained segment, a
  snapshot with no log suffix, and rooted log-only history.
- Offset overflow, a missing manifest, a suffix gap, unsafe member paths, and the
  pinned two-digit and 62-digit object path format.
- Content rewrite, removal, and rotation against the whole supplied inventory.
- Mixed checkpoint generations and source scopes, changed caller or getter
  slices, combined member and manifest quotas, and configuration hash mismatch.
- Unknown, missing, duplicate, null, case-aliased, and crossed source fields.
  Initialized source decoding rejects members the structural selector would drop.
- Unpublished records contain no invented native database, owner, module,
  replica, snapshot, or framing facts.

These tests use synthetic member records. They establish no descriptor ownership,
native framing, object completeness, recoverable point, or exact coverage.
Contained production-reader replay, immutable source and remote equality,
managed jobs and credentials, scoped real S3, and physical T35-T41 acceptance
remain required. Native source hardlinks require a separately qualified host
copy boundary before the private one-link member contract applies.

## Typed engine boundary evidence (P09-01 / T34)

Closed engine records reject unknown, missing, null, duplicate and mismatched tags. SpacetimeDB structural declarations and sources have no SQLite filename, catalog, PRAGMA, LTX or Litestream requirement, while current spec/runtime admission stays disabled. Unsupported observation/capture/verification refuses before binding/credential/workspace effects. SQLite writer/replica permits require the matching engine and shared identity/fence/ownership proof before exact Litestream runtime checks.

`internal/data/ownership_test.go` and `internal/restore/ownership_test.go` mutate caller/getter evidence and prove retained source, barrier, declared invariants, coverage, sentinel and nested allocation proofs stay unchanged. The verifier still enforces a two-row requirement after the caller changes its original slice to one row. Null negatives cover every required concrete binding/source/barrier field and invalid binding/epoch tokens. Independent overlay reproductions pass after these fixes.

`internal/plan/testdata/sqlite-allocated-*.json`, `sqlite-unknown-*.json` and `internal/restore/testdata/sqlite-engine-receipt.json` preserve actual pre-extraction bytes. Independent review compiled the actual base with the same fixtures and confirmed all five outputs. `scripts/check-engine-boundary.sh` forces golden regeneration off even under inherited update settings. Full regression tests, Linux/darwin vet, CLI build, static authority/dead-code/clone gates and focused race tests pass. T34 claims local SQLite preservation and fail-closed typed dispatch only; physical SpacetimeDB acceptance remains T35-T42.

### T36 fixture artifact, client and publication cases

Run `fixtures/spacetimedb-app/build.sh` with a new private build directory and
`smoke.py` against its pinned runtime. The latter imports the shared production
observer through `tools/spacetime-runtime`; it does not reproduce its renderer.

- Distinct mutually exclusive module variants and identical independent v1
  builds. Retain artifact SHA-256 and explicit module/client pairing; the native
  program identity uses its own Keccak and wire representation.
- Base and additive module clients decode the same paired rows. A defaulted
  column uses regenerated official bindings, preserves prior column order and
  default seven, and is not accepted by the published base client image.
- An outsider's write refuses, excess schedules refuse, and client writes
  refuse while the bounded scheduler writes without connected clients.
- An actual browser receives an explicitly confirmed WebSocket subscription
  and records a write only after the matching confirmed marker. Its user input
  is cleared and browser storage stays empty. An unconfirmed acknowledgement
  cannot construct recovery evidence.
- A stopped private tree copy replays identical paired sequence/marker/time
  values and the complete shared canonical/module/owner/name observation.
  A marker read on the base fixture does not change rows. A separate lifecycle
  candidate changes the marker during connection and is refused by the fixture
  invariant reader.
- Preflight keeps source rows unchanged. Nonempty table removal and access
  changes require independent Brine review even when native preflight says
  automatic. Uncertain or merely clean candidate shutdown supplies no exact
  capture receipt by itself.
- Public app configuration has only endpoint/name fields. Publisher/signing/S3
  material never enters Wasm, browser assets, app layers or retained workflow
  evidence. App startup never publishes a module.
- The main-only publication job consumes verified bytes, records actual GHCR
  index and both platform digests, and binds the source revision and retained
  module digest. Execute the published amd64 and arm64 images; cross-builds
  alone do not establish either container runtime or remote restore acceptance.

### Protected runner storage qualification

Local tests refuse arbitrary storage-profile paths, ambient profile redirection,
missing or unprotected configuration, a changed canonical enrollment hash,
unqualified FUSE package and binary versions, a store without dynamic UID
mapping, additional image stores, and a character device other than Linux FUSE
10:229. Enrollment order and its interruption matrix include the protected
profile before rootless queries. Undo converges after directory removal and
still removes a validated pending empty directory. Rendered application tests
read `[Container]` environment separately from the fixed Podman `[Service]`
profile.

The authorized isolated-driver probe used the actual source command runner and
local index/platform/image verification. Native overlay CREATE exceeded its
300-second diagnostic cap. FUSE CREATE of the same image succeeded in 0.26
seconds with its unchanged 60-second deadline, then exact cleanup preserved the
original image/layer metadata and payload identities. This proves the storage
mechanism, not a native child, deployment, backup, restore or production profile
transition. Those require the separately authorized physical CLI flow.

### Namespace-local native binding

The authorized metadata-only probes showed a live owned helper with valid
PIDfd, cgroup, process identity and executable. Host traversal of its FUSE root
failed for the runner and root observer. A fixed no-argument program executed
as UID and GID 1000 inside the same container reported the exact host data, key
and executable device/inode identities with the correct namespace owner view.
All disposable processes, containers, mounts and files were removed; original
control and image payload evidence remained intact. Shared container metadata
mtime changes were recorded rather than restored to force equality.

Local tests refuse overlapping or overflowing UID/GID maps, forged namespace
lifetimes, foreign device/inode/type/mode/owners, unmapped-owner claims when a
map actually covers that owner, regular size or link-count drift, missing or
extra members, malformed and duplicate frame fields, arbitrary helper arguments,
and missing, duplicate, foreign or writable helper mounts. Held-descriptor tests
reject regular-file drift while directory size/link changes from ordinary live
writes retain the existing directory identity contract. This is binding-source
and mechanism evidence; actual native initialization, publication and remote
recovery still require the authorized physical command flow.

### Refusal diagnostics and accepted-task locators

Local tests cover a successful attach carrying a closed guest refusal, retaining
that enum through cleanup without producing owner evidence. Unknown, malformed,
duplicate or extra-field diagnostics cannot replace a typed primary error or
supply a phase code. Tests also cover preserving a validated accepted operation
ID through terminal failure, observer cancellation and invalid status while
retaining the original typed error, code and exit status. The error remains
`ok=false`; unvalidated acceptance and crossed status IDs never supply a locator.
A complete CLI test submits once, polls once and reports the failed resolver's
accepted ID without replay. These tests explain a recorded helper exit 1 after
attach exit 0; they do not identify the underlying guest refusal or prove a
successful native operation.

### Native child startup transition

Local subprocess tests hold a PIDfd and start ticks across a real exec
transition, then require the exact executable and arguments. Foreign paths or
arguments, changed ticks or cgroup, a dead process and an expired deadline
refuse. Readiness cancellation leaves the enclosing child lifetime intact.
Listener readiness receives the same startup deadline after command binding;
it cannot obtain another full startup budget. Closed diagnostic tests preserve
the first binding, listener, publisher or operation failure when child cleanup
also fails.
Initial live handoff tests refuse foreign container IDs, process IDs, images
and mounts without choosing another candidate or issuing a mutation. Shared
command-wait tests refuse non-command guard drift while readiness is pending.

The authorized dummy native probe used the pinned image and production
resources, mounts, UID, argv and environment with fresh disposable data and
signing keys. Its immediate command check failed while all other binding
predicates passed. The same child then reached its listener and exact command
identity in 1.538 seconds, and stopped cleanly. Actual cleanup preserved control
records and original image payloads. This proves the startup transition, not
publisher verification, native preparation, publication or remote recovery.

### Unpublished owner retention and upload

The physical CLI21 flow earned a bound native child, successful publisher
observation, helper disposal and one immutable initial owner. It then failed
before the upload intent. A read-only detached check verified the owner-only
phase, stopped source and material, exact source reconstruction, transport
deadline and local repository constructor. It issued no helper, restic command
or network call. Local bundle packing passed. An exact-driver reproduction
failed the upload INSERT because absent module bytes bound SQL NULL to a
required blob column.

Local tests now store absent and present upload payloads and prepared evidence
through the actual INSERT boundaries, assert blob types and unchanged bytes,
and reject other issued effects in the owner-only phase. Owner continuation
tests reconstruct its exact generation and signing-recovery form, preserve the
owner receipt and original failure, bind ownership into review hashes, and
refuse changed generation, source, seal or material. The continuation reaches
capture admission without another initial stage or helper. Its new workspace is
journaled; unknown cleanup observation preserves that journal rather than
claiming disposal. These tests and the read-only diagnostic do not prove a
successful remote upload or prepared point.

### Publication review of a resolved preparation

The captured physical store contains a verified prepared point and succeeded
resolver receipt while preserving the original failed preparation. Exact source
validation passes that provenance; a review query requiring the original
operation to be `succeeded` returns no row. Local Store tests now begin and
recheck review from the validated resolved point, preserve the original failure
and refuse a missing resolver or changed completion proof. The ordinary
succeeded producer remains covered. Point expiry and all other review scope
checks remain unchanged. This is review admission evidence, not publication or
deployment success.

### Prepared point fence owner

The captured fresh CLI23 point has a succeeded producer and the allocation's
retained held fence. The source reopen query using the producer as fence owner
matches zero rows; the validated point's recorded fence owner matches one.
Local tests exercise that acquisition boundary with real allocation/material
state and a distinct succeeded fresh producer. They require the retained owner,
refuse a wrong owner, fence or binding, and preserve allocation, runtime and
fence records. Earlier point provenance checks still run before reopening.
No helper, native program or remote call is needed for this proof.

### Private staging parent lifecycle

The preserved CLI24 host has no state-root `staging` directory. The source copy
boundary requires that directory before copying, and enrollment does not create
it. Local tests reproduce that missing-parent refusal, then copy real fixture
bytes and materialize a selected reader view through the protected parent
writer. Consumer cleanup removes a newly created empty parent. Existing parent
contents, unsafe modes or types, symlinks, foreign-owned roots and replacement
directory contents are preserved. Canceled setup and failed consumers leave no
new empty parent. These are local filesystem proofs; they do not claim a module
plan, publication or deployment succeeded.


### Synchronous review budgets

The CLI25 physical module plan restored its remote point and started descriptor
extraction, then the enclosing SSH observer expired at 15.038 seconds. No review
or apply record existed. The retained task resources were reconciled and cleaned
without retrying the plan.

Local dispatcher tests check the nine-minute budget for exact-point review,
earlier parent deadlines, parent cancellation and cancellation during request
reading. An ordinary host request spends time reading its input and still uses
the original fifteen-second deadline. Fake SSH tests assert the complete ten
minute thirty second review budget, the ordinary fifteen-second budget, earlier
parent deadlines and one call without retries. A real filesystem fixture invokes
publication review cleanup after cancellation, removes the owned copied source
and newly created empty staging parent, and preserves the original source. These
checks do not establish a successful remote module plan or publication.

### Synchronous material delivery budget

The CLI37 physical initial material call ended with `transport_failure` after
15.066 seconds and no operation response. The new-engine candidate pulls its
selected pinned image before reserving material custody. The ordinary request
budget cut off that pull despite its intended two-minute bound.

`native_material_set` now allows two minutes for the pull and fifteen seconds
for the other host work. The client allows two minutes forty seconds in total,
including input reading, the existing five-second custody reconciliation and SSH
connection allowance. Only the pull receives a longer subprocess session budget;
other image probes and metadata status keep their existing bounds.

Local dispatcher tests inspect the actual work deadline and earlier caller
deadlines. A client-to-dispatcher fixture completes a sixteen-second delivery,
then verifies an earlier caller deadline still cancels both layers with one call
and `transport_failure`. A real control-store fixture cancels the pre-custody
pull and verifies no material reservation exists for its exact key. Existing
immutable receipt and unknown-write tests remain required. These tests do not
establish successful physical delivery, image-pull cleanup or publication.

### Detached task state handoff

The CLI26 actual module review succeeded. Its accepted apply operation entered
preflight, then became recovery-required without a publication claim, cursor or
effect. A local diagnostic used the real Runner, Store and module apply guard.
It proved the handler received queued while the durable operation was preflight,
so the guard refused before host work.

The runner now updates its local operation state only after the successful
preflight transition. Real Store regressions cover queued and launch-unknown
handoff, unchanged operation identity, durable failure outcomes and no terminal
replay. A competing claim wins the actual Store transition first; the losing
runner invokes no handler and records no outcome. These checks do not prove
publication success or change the original physical failed operation.

### Detached module task requester

CLI27 earned another actual module review and approval. The new accepted apply
again entered preflight and failed before any claim or effect. DetachedRunner
opens the mutation runtime without a dispatcher requester. A read-only captured
Store diagnostic exercised the real module guard and current policy: its empty
ambient requester refused the saved review, while the exact accepted operation
requester matched the review, approval and policy checks.

Module apply and resolve now reconstruct a task-local service requester only
from matching immutable operation kind, preflight state, app, review ID and
review requester. Both keep current policy and publication inspection checks
before host work. Neutral valid-plan tests cover empty and unrelated ambient
requesters, both task kinds, isolated service copies and foreign job/review
bindings. Captured-state checks exercise the full protected-store boundary and
current policy refusal. Original failed operations and the no-claim resolver
limitation remain unchanged. This is admission evidence, not publication success.

### Publication working path provenance

CLI28 earned a real claim, restored-stage proof and initial-create preflight
observation. Its publish-stage intent became inspection-required before an
effect row or container launch. The protected restore constructor opens through
a descriptor, so its Root.Name contains a proc-fd spelling. Effect admission
inferred a working pathname from that name; its named-path checks refused it.
A local real-constructor diagnostic reproduced this exact refusal and showed the
constructor's explicit physical path matched the held inode and protected root
inspection.

Both publication review and attempt working-root binding now retain that
explicit physical path after authenticated-copy, parent-child, named-root and
held-inode checks. The capability rechecks it before effect admission. Local
real Root tests accept the physical path and refuse proc-fd inference, a foreign
inode, directory replacement and symlink while preserving the held original.
Existing authenticated-download refusal and process-effect disposal tests pass.

A copied actual claimed Store admitted the existing resolver and private
inspection capability with no native effects and preserved the original failure.
That is Store admission evidence only. The actual public resolver subsequently
failed without completing source preservation; its cursor and claimed workspace
remain retained. No successful publication or resolution is claimed here.

### Source-preservation proof migration

Both fixed read-only host checks for the failed CLI28 resolver passed. The exact
captured-store preservation proof INSERT then reproduced SQLite CHECK error 275:
the proof-kind list admitted no `source_preservation`, which the current resolver
writes before changing its cursor or fence.

Schema 27 adds that one proof kind through normal writable Store.Open migration.
Local schema-26 fixtures require prior rejection, unchanged earned proof bytes
and identity/count, then successful insertion after migration. Tests retain
unknown-kind refusal, primary/foreign keys, hash/byte limits and update/delete
refusal. A failed final foreign-key check rolls the migration back to schema 26,
preserves retained bytes and restores foreign-key enforcement. Canceled opening
leaves the schema unchanged. Read-only opening refuses schema 26 and accepts 27
without granting writes.

On a disposable copy of the actual post-resolver snapshot, normal Store.Open
preserves both earned proof records exactly. The real proof insertion, cursor
append, stopped-desired record and fence-release transaction substeps pass and
roll back. This verifies the database boundaries; no host source-preservation
completion, resolver success or publication is claimed.

### Fresh preparation after preserved unpublished source

CLI30's actual resolver completed source preservation and released its claimed
allocation fence. A distinct fresh preparation then failed before native work.
The captured-state diagnostic passed task input, runtime, pending-work and
immutable unpublished-owner guards, then reproduced refusal because no held
allocation fence remained.

A normal new preparation now owns a new fence after every current-epoch
consumed publication has a terminal preserved-source cursor and matching proof,
original failure and succeeded resolver receipt. Admission rechecks the exact
unchanged unpublished runtime and stopped desired state under the existing
binding guard in the same write transaction as fence creation. It preserves the
released consumed fence and can reuse only the same operation's new fence.
Initialized preparation keeps its current path.

Neutral tests check terminal cursor proof hashing and exact winning resolver
identity, including unfinished/foreign resolver refusal. On a disposable actual
post-preservation copy, tests exercise new fence holding, same-operation reuse,
protected owner derivation and BeginNativePreparation. Missing/changed proof,
unfinished resolver, foreign task, changed original outcome/runtime/desired
state and another unfinished consumer refuse without a new held fence.
Concurrent same-operation holding converges on one fence; another owner refuses.
These are admission proofs, not a successful new remote preparation.

### Publication refusal codes and native offset caches

CLI31 earned the first real publication effect with helper start, attach, exit1,
disposal and unknown events. Its private module log recorded table creation and
database initialization, but no publication receipt or target transition exists.
The controller discarded the guest's fixed refusal code for publication effects.
Unknown effect events now optionally retain only a closed action and fixed phase.
Absent diagnostics preserve the exact retained canonical shape. Tests reject
unknown actions/codes and foreign steps, keep the primary phase over cleanup,
and preserve underlying errors. Create artifact, publication and observation
boundaries emit distinct fixed guest codes without changing native calls.

The admitted passive classifier then stopped at inventory, before any reader or
view was created. The actual working tree has a same-offset `.stdb.log` and
4MiB `.stdb.ofs`. Pinned upstream paths define the latter as an offset index;
its stream reader uses it only as an optional seek optimization. A real-file
fixture reproduced the old inventory refusal. Inventory now omits only a safe,
canonical, paired offset index while keeping the whole-tree seal and strict log
selection. Tests retain the active log tail, original cache bytes and rejection
of orphan/malformed/unknown members. This does not classify the actual native
publication. The protected reader image is separately confirmed absent; its
existing reviewed pin still governs dependency setup.

### Retained evidence keeps issued reader limits

The actual CLI32 resolver's reader succeeded and was disposed, while its
preservation still failed before proof insertion. A fixed read-only checker
passed every one-effect anchor and final source, policy, route and stopped-host
guard. Captured candidate-shaped transaction substeps also passed and rolled
back. The carried evidence decoder then reproduced the refusal: native reader
limits are smaller than global maxima, and decoding requires an exact requested
limit match. Store rereads incorrectly supplied the maxima.

Opaque evidence now rereads with its exact bound requested limits. Protected
stored bytes use a separate strict decoder that validates declared limits
within global maxima, then invokes the unchanged exact-limit decoder. Live
reader output still uses its issued limits. Tests preserve canonical bytes,
hashes, state and maximum-bound records, reject changed live request limits,
malformed/zero/oversized or insufficient record limits, aliases/duplicates/nulls
and output/table/row overflow. Changed owner content produces different identity,
state and hash; parsing grants no provenance.

A private captured-review fixture exercises the real candidate/owner acceptance
boundary with syntactic evidence under production reader limits and rejects a
foreign owner. It does not fabricate native data, physical anchors, a completed
preservation transaction or receipt. No new reader or native helper ran.

### Native child shutdown reasons

The fresh CLI33 preparation and review succeeded. Its new publication effect
retained `guest_child_stop` after successful create/observation, then failed
without a live transition. The helper's lifetime was 5.367 seconds and attach to
exit was 2.254 seconds against a 30-second stop budget. These facts do not locate
the child wait error. Native stdout and stderr were previously discarded.

Shutdown diagnostics now distinguish interrupt refusal, caller cancellation,
stop-budget exhaustion, nonzero exit and signal categories. The module helper's
bounded stderr sink keeps at most 64 bytes of transient overlap and reports only
a fixed panic marker. It detects markers across writes and late in a stream;
no native text enters the journal. Clean stops still succeed, every failed stop
still refuses, and original wait errors unwrap unchanged. The existing SIGINT,
budgets and forced cleanup remain.

Focused real subprocess tests cover clean/nonzero waits, SIGINT/SIGKILL/other
signal categories and forced stop after the budget or canceled context. Tests
also cover split/late panic markers, bounded retention, redacted frames and
observation-first failure precedence. These diagnostics do not prove the actual
shutdown cause or a successful publication. The new CLI33 claim was separately
preserved by its supported public resolver; its working copy remains retained
for the next diagnostic consumer.

### Initial publication before service installation

`TestInitialPublicationObservesAbsentServiceUntilOrdinaryDeploy` reproduces the
fresh allocation boundary: strict initialized readback refuses the absent unit,
while initial publication observes its absence without writing a service. It
refuses an unexpected installed service, and initialized publication requires
exact installed bytes. `TestInitialPublicationRefusesUnsafeUnitAndChangedServiceDeclaration`
refuses a dangling unit symlink and a changed canonical unit declaration.
`TestPublicationUnitAbsenceBelongsOnlyToUnpublishedSource` checks the source-state
admission rules; `TestPublicationActivationRequiresTheRecordedUnitObservation`
checks that activation cannot substitute unknown or installed state for recorded
absence. `TestPublicationCompletionReadbackRetainsInitialUnitAbsence` reads a
serialized completion through the receipt validator and rejects swapped unit
observations. Physical publication acceptance still requires the full P09-08 drill.

### Rootless Podman user bus prerequisite

The minimal Debian amd64 fixture had a running, lingering user manager and
`dbus-daemon`, but no `dbus-user-session`, user bus socket or user D-Bus units.
Podman explicitly reported fallback from systemd to cgroupfs. Fresh enrollment
now includes the missing package in its exact reviewed transaction. Tests cover
read-only probing without recommendations, missing or unqualified candidate
refusal, and installation with the original journaled dependency versions.
These package-boundary tests do not prove a successful native preparation.

An independently named neutral container on the current pinned amd64 image,
with the same private namespaces and security profile, then reproduced two
standard masks absent on the Pi: empty read-only tmpfs `/proc/acpi` and
read-only `/null` devtmpfs `/proc/kcore`, a null character device with major 1
and minor 3.
The prior closed module mount classifiers rejected both before input. The
shared guard now requires their exact path, backing filesystem, mount mode and
non-symlink node metadata, with a bounded actual directory emptiness check.
Tests reject writable, foreign and changed mount shapes in both helper profiles,
wrong/null/block/symlink/device/owner/link metadata and actual nonempty
directories. The neutral container and scope were removed. This is containment
evidence, not recovery of the original unknown operation or a publication receipt.
