# Phase 03 Pi run

Recorded 2026-10-09. The first attempt used `9670c1a`. The resumed attempt merged `c4caf3d`, including stateless removal, and tested the Docker Hub fix in `9e862a1` on the physical target.

## Latest result, 2026-10-10

**Real v2-to-v3 migration and terminal-removal resolution passed. Clean-fixture recreation is blocked by retained-secret inventory after a nonzero control generation. The Phase 03 exit gate remains open.**

The current run on `52977d3` preserved the existing receipt and all 13 event rows through migration, then completed its removal through `brine resolve` without operator app mutations. There are zero committed apps and no fixture unit/container/selected route/listener. D5-retained secret v1 and history remain. Planning the recreated fixture now refuses `unknown_facts` for `app.image`, `app.port`, and `apps.port`; even a different app with no secret references refuses `apps.port`. The lane reproduced the same condition locally and stopped rather than delete retained secrets, reset control generation, or invent runtime absence. Details follow at the end of this file. No new deploy was accepted, reboot issued, or acceptance checkbox changed.

## Earlier result, 2026-10-09

**First stateless deployment verified; blocked on route provenance before updating it. The Phase 03 exit gate had not passed.**

After a separate operator session prepared enrollment and routing/TLS prerequisites, the real restricted client deployed a digest-pinned fixture with an immutable Podman secret. Direct HTTP and normally verified routed HTTPS both returned 200. Two small production defects were reproduced with failing-first tests and fixed: Docker Hub resolution and Caddy route permissions under the private job umask.

The next `config set` plan refuses with `artifact_drift` for `caddy.live`, `domain_owned`, and `unknown_facts` for `live_caddy_files.domains`. The production inventory cannot attribute an imported managed route to its app and leaves root domains unknown. This is a larger provenance problem; the lane stopped rather than weaken route ownership checks. No release replacement, explicit rollback, client kill, injected runner kill, or reboot was performed. Both permitted reboots remain unused. P03-04 remains open for existing-data mounts and writer-switch evidence; no acceptance checkbox changed.

Fixture cleanup through the real `remove` client withdrew the route and stopped the writer, but reached `recovery_required` at `stop_unit`. Read-only inspection identified a third small defect: the production systemd queue reader expects numeric zero, whereas systemctl prints an empty labeled `Job=`. The fix retains strict failed/missing-output rejection and is tested against actual property serialization. Cleanup outcome after that fix is recorded below.

## Evidence conventions

`$OPERATOR_TARGET`, `$TARGET`, `$CLIENT_DIR`, `$DEPLOY_PUBLIC_KEY`, and `$SCRATCH` replace private target aliases and paths in commands. Account inventories, addresses, keys, and secret values are omitted. Fixed paths below belong to Brine's implementation. `fixture.brine.test` is the synthetic test route, not the host's name or address.

Previous runtime evidence is in [the Pi runtime spike](../spikes/pi-runtime.md). That spike does not prove the current production deploy path works. There was no `docs/evidence` directory in the first checked revision.

## Initial installation blocker

The installed binary matched its root-owned enrollment record and reported `0.1.1-dev`. That version string does not identify its source commit. The target lacked the current protected policy, requester, authentication marker, boot recovery files, and control database.

The first current-client observations returned:

```json
{"schema_version":1,"command":"brine status","ok":false,"data":null,"error":{"code":"dispatch_operation_refused","message":"The dispatcher operation is not allowed.","retryable":false}}
```

```json
{"schema_version":1,"command":"brine reconcile","ok":false,"data":null,"error":{"code":"dispatch_operation_refused","message":"The dispatcher operation is not allowed.","retryable":false}}
```

Supported re-enrollment reached target-name confirmation, then exited 1:

```sh
"$SCRATCH/brine" enroll "$OPERATOR_TARGET" \
  --target-name "$TARGET" --deploy-key "$DEPLOY_PUBLIC_KEY" \
  --host-binary "$SCRATCH/brine-arm64" --config-dir "$CLIENT_DIR"
```

```text
enrollment options differ from durable intent
operator SSH command failed; inspect and reconcile the host before retrying
```

`internal/enroll/host_linux.go:359-360` refuses a changed binary hash after recorded binary intent. [The enrollment contract](../CONTRACTS.md#operator-enrollment) explicitly excludes treating a different binary as an enrollment rerun. A documented operator update path remains missing.

A separate operator session reported that supported undo also refused with `unrecorded runtime data refused`, then Caddy tree drift. That session reported manual withdrawal of journaled artifacts and fresh enrollment. This lane did not perform or independently verify that withdrawal. Those reports are follow-ups to reproduce, not evidence that every used host is impossible to unenroll. The installation and undo findings remain under P06-03 and P06-05.

## Resumed baseline

After the separate operator setup, the new restricted client returned:

```json
{"schema_version":1,"command":"brine status","ok":true,"data":{"apps":[]},"error":null}
```

```json
{"schema_version":1,"command":"brine reconcile","ok":true,"data":{"control_state":"preview_unavailable","dry_run":true,"outcomes":[]},"error":null}
```

The observed preview was `preview_unavailable`, not the `database_missing` reported by the setup session. This run did not investigate that difference. It does not count as a successful effect-boundary preview or prove database preservation.

No existing v1 database was present. Physical v1-to-v2 migration evidence is omitted rather than manufacturing a preexisting journal. Automated migration tests remain the evidence for that behavior.

The target reports Debian GNU/Linux 13, trixie, full version 13.6, on arm64. Exact installed package revisions were:

```text
caddy 2.6.2-12+deb13u1
passt 0.0~git20250503.587980c-2+deb13u1
podman 5.4.2+ds1-2+b2
systemd 257.13-1~deb13u1
```

Litestream was not queried. No backup or R2 restore was attempted.

## Fixture and Docker Hub regression

The fixture was `nginxinc/nginx-unprivileged:stable-alpine`, resolved before planning to the immutable multi-architecture index:

```text
index    sha256:15c994d10d6d78658721c3bcafff14cb281fba2a4bdf9d5ba92c416a472516e3
arm64    sha256:4538a98ff4262f70babae95a74ecc5cff3de466445f4ef022cc42be5a29b10c2
config   sha256:1d8171baa86a607971182e96563f06667b683d900eba9f799d3b818eee336c21
```

The index contains linux/amd64 and linux/arm64 entries. The TOML used only the immutable index, container port 8080, `fixture.brine.test`, a 30-second startup deadline, HTTP `/` with status 200, an environment release marker, and the allowed `fixture-token` reference. At the initial planning attempt, no image pull or container startup occurred.

A generated secret value reached only stdin of the real restricted client:

```sh
"$SCRATCH/brine" secret set fixture fixture-token \
  --target "$TARGET" --config-dir "$CLIENT_DIR" --json < "$PRIVATE_SECRET_FILE"
```

Captured response:

```json
{"schema_version":1,"command":"brine secret set","ok":true,"data":{"operation_id":"01a1219c093bb596dc198ec3a4bfca754993cebab3ed","version_name":"brine.fixture.fixture-token.v1","bound":false},"error":null}
```

The first connected Docker Hub plan returned:

```json
{"schema_version":1,"command":"brine plan","ok":false,"data":null,"error":{"code":"internal_error","message":"The operation failed.","retryable":false}}
```

The resolver treated `docker.io` as the registry API origin and only allowed same-origin anonymous token services. Docker Hub instead uses `registry-1.docker.io`, `auth.docker.io/token`, and blob redirects to its CDN. Failing-first tests reproduced the incorrect registry host and rejected CDN. The fix maps only the Docker Hub alias, admits its exact anonymous token endpoint and service, and admits exact HTTPS Docker CDN hosts for blobs only. Redirected requests strip authorization. Tests still refuse unrelated hosts, alternate ports, token redirects, and manifest redirects. All digest and platform checks remain in place.

The Docker registry config blob actually redirected to `production.cloudfront.docker.com`. The fixed production resolver completed connected planning against the same real index. No credentials or registry token were captured in published evidence.

## Authorized test binary installation

The task authorizes operator installation of the test binary. Because no supported update command exists, this run used the minimal explicit replacement below, after local gates passed. The original fresh-enrollment executable remains preserved in the separate operator setup workspace.

```sh
scp "$SCRATCH/brine-arm64" "$OPERATOR_TARGET:/tmp/brine-p03-fixture-binary"
ssh "$OPERATOR_TARGET" 'sudo -n install -o root -g root -m 0755 /tmp/brine-p03-fixture-binary /usr/local/bin/brine && rm -- /tmp/brine-p03-fixture-binary'
```

Only the Brine binary was replaced. The protected enrollment record was not rewritten. This manual test installation is not a supported journal-aware upgrade and leaves its recorded binary hash stale. No claim of working upgrade or supported undo is made.

## Routed planning conflict

After the Docker Hub fix and binary installation:

```sh
"$SCRATCH/brine" plan "$SCRATCH/fixture.toml" \
  --target "$TARGET" --config-dir "$CLIENT_DIR" --json
```

Captured response:

```json
{"schema_version":1,"command":"brine plan","ok":true,"data":{"plan_id":"sha256:47dc8ab58aa211a9ab4950c2db17242ddd6132617f8560cd180d75f3e028c602","kind":"conflict","diff":null,"conflicts":[{"code":"domain_owned","field":"domains"}]},"error":null}
```

Read-only `caddy adapt` of the operator's root configuration showed one server listening on `:80`, with an empty route matcher and `vars` plus `file_server` handlers. Its site has no host restriction. The planner's domain ownership checks at `internal/plan/plan.go:292-306` conservatively treat that site as owning the requested domain. The selected Brine generation remains 0.

This is a routing precondition, not a reason to silently remove an operator site. A usable non-public fixture also needs a trusted local HTTPS certificate because routed health uses normal TLS verification. At that stage, the lane had not established that certificate prerequisite or attempted issuance. Readiness work under P06-02 records both prerequisites.

## First physical apply and private-umask regression

The separate operator session reported removal of the hostless operator site, a root `local_certs` block plus Brine import, validated Caddy reload, and local-authority trust installation. This lane did not perform those preparation actions or amend D3. Read-only root validation passed; `local_certs` caused no planner/parser objection.

The real create plan had no conflicts:

```text
plan sha256:7207b754cbda0f183fc9a0214ac6991abc38a6a6b6e593a3113635f081267e21
first operation 01a121a5f56d552a11079141ad2ac68042f1f7d91b4a
```

The first apply pulled and verified the arm64 image, bound `brine.fixture.fixture-token.v1`, installed/started the Quadlet, and passed direct health. Routed health timed out after 30 seconds. Reverse rollback completed with terminal `rolled_back`; the operation did not falsely succeed. This was a first-release rollback to absence, not restoration of a previous healthy app. Durable status remained readable after the transient unit was collected.

Caddy's reload succeeded but reported no matching import files. Detached jobs use `UMask=0077`; the generated directory and app route were actually 0700 and 0600, inaccessible to the separate Caddy service account. The generation writer had requested 0755/0644 without explicitly restoring those permissions. A subprocess-isolated test under umask 0077 failed before the fix. The writer now creates privately, explicitly sets generation directories to 0755 and route files to 0644, and keeps whole-root candidate files private 0600. The detached job umask remains 0077. No unrelated Caddy site is modified.

After installation of `d35f65a`, the same create plan succeeded:

```text
operation 01a121ac2b04ed4d71e600e38f8c5dc4f3aa09e8c69a
accepted 2026-10-09T17:18:13.764906337Z
succeeded 2026-10-09T17:18:23.068338402Z
```

Read-only physical inspection confirmed generation 2 directory 0755, `fixture.caddy` 0644, and whole-root candidate 0600. Both probes used normal verification, with no insecure flag:

```sh
ssh "$OPERATOR_TARGET" 'curl --noproxy "*" -fsS -o /dev/null -w "%{http_code}\n" http://127.0.0.1:20000/'
ssh "$OPERATOR_TARGET" 'curl --noproxy "*" --resolve fixture.brine.test:443:127.0.0.1 -fsS -o /dev/null -w "%{http_code}\n" https://fixture.brine.test/'
```

Each returned 200. Restricted `status fixture` reported the committed image digest, active unit and healthy direct probe. `diagnose fixture` reported a running container and present route, but drift remained unknown and the bounded log tail returned `internal_error`; that is not full diagnostic acceptance. The active Caddy root fingerprint matched the installed system trust root. Secret values were never printed or compared in public evidence.

## Larger update blocker: imported route provenance

```sh
"$SCRATCH/brine" config set fixture environment.RELEASE=two \
  --target "$TARGET" --config-dir "$CLIENT_DIR" --json
```

```json
{"schema_version":1,"command":"brine config set","ok":true,"data":{"plan_id":"sha256:02caf962721d6eb02853e555caf1710d465ae97e00e27cb7dac41bdb6117c857","kind":"conflict","diff":null,"conflicts":[{"code":"artifact_drift","field":"caddy.live"},{"code":"domain_owned","field":"domains"},{"code":"unknown_facts","field":"live_caddy_files.domains"}]},"error":null}
```

No apply was launched for this plan. The deployed route was healthy and its artifact permissions correct. `internal/inventory/artifacts.go:257-274` emits generic hashed names with no app ownership for imported files and leaves the root file's domains unknown whenever imports exist. `internal/plan/plan.go:462-485` requires a live file matching the committed app and route filename; `:292-306` refuses unknown or foreign domain ownership. Existing host tests supply already-attributed synthetic snapshots, so they do not expose this production mismatch. A safe fix must establish file provenance while preserving unrelated imported/root sites and whole-live-config verification; simply declaring the root empty or ignoring unknown domains is not acceptable. The owning Phase 03 task records the reproduction and acceptance needed.

## Removal and remaining resources

Initial removal before deployment correctly refused `unowned_app`. After the healthy first release, real removal planning returned `kind:update`, no conflicts, with secret retention `d5_retained_releases`:

```text
plan sha256:f3da8c4c68cf567ceb3c764819a9e3396ad4778be972b5d6ec31e1324e437ddb
operation 01a121b1e29e45390eb6945d6b142cabb2c2d61c6584
```

`withdraw_route` completed. `stop_unit` recorded `unknown` with code `interrupted`; terminal state was `recovery_required`. Read-only inspection showed `ActiveState=inactive`, `SubState=dead`, no Podman containers, and no selected `fixture.caddy`. Direct connection was refused. The committed release head and allocated port were not retired. Dry-run reconciliation returned no outcomes; it did not resume this terminal recovery-required operation. This is safe fencing, not successful removal acceptance.

The real systemd serialization was:

```text
LoadState=loaded
Job=
```

The old `JobPending` implementation queried `--property=Job --value` then parsed only an integer. Failing-first tests reproduced rejection of the actual settled-job output. The corrected reader requests labeled LoadState and Job together, accepts an explicit empty Job only with known loaded/not-found state, parses bounded numeric pending IDs, and rejects missing, duplicate, malformed, overflowed or failed reads. This is current systemd behavior, not mixed-version fallback.

### Cleanup follow-up after the parser fix

The fixed arm64 binary was installed through the same authorized binary-only replacement. No DB events or provenance records were manually edited. A new removal plan returned a conflict, not permission to replay the earlier partial operation:

```text
plan sha256:b6bfd59b7a99eee60454ba7984f649d7487b6db6567355db6dc0630b3f7efd4c
kind conflict
artifact_drift:route
```

Dry-run reconciliation again returned no outcomes because the earlier operation is terminal `recovery_required`. No conflicting plan was applied and no irreversible action was blindly retried. The corrected parser passes local regression tests, but could not complete this already-terminal removal through the supported API; physical successful removal remains unverified.

Final read-only inventory: `fixture.service` loaded/inactive/dead with an explicit empty Job, no Podman containers, no fixture route in the selected generation, and direct port 20000 refusing connection. The retained `fixture.container`, historical generation-2 route, pinned image, immutable secret v1, committed release head, reserved port, and recovery-required removal history remain. The control DB is preserved. Do not describe the task as clean or the residual secret as unbound: it is bound in retained release history. No account, root Caddy config, unrelated route, image cache, secret, or enrollment provenance was manually deleted.

## Acceptance still owed

T07 first stateless release is physically verified. The routed-health failure demonstrated no false success and first-release rollback to absence, but does not replace T08 invalid-release/previous-release restoration or T11 controlled Caddy failure evidence. T04, T08-T11, T16-T17, T21-T22 and the full exit gate remain owed. Secret bootstrap/binding is only partial P03-08; config change and secret rotation are not verified. Existing-data mounts and schema-breaking migration remain Phase 04. No zero-downtime or R2-restore claim is made.

## Local verification

Docker-specific and restrictive-umask regressions failed before their fixes and passed afterward. The original and resumed code gates passed all 30 Go packages, vet, client build, Linux arm64 cross-build, empty formatting, diff check, and Darwin arm64 vet. The systemd regression likewise failed first. Final verification passed `go test ./...` (30 packages), `go test -race ./...` (30 packages), `go vet ./...`, `go build ./cmd/brine`, the Linux arm64 cross-build, empty `gofmt -l ./cmd ./internal`, `git diff --check`, and `GOOS=darwin GOARCH=arm64 go vet ./...`. The targeted actual-systemd serialization regressions also passed with `-count=1`. Checks used `GOTMPDIR` under the private task scratch and `GOCACHE=/work/cache/go-build`. An initial local rerun hit the host's `/tmp` disk quota; subsequent build scratch was redirected to `/work`, without deleting other sessions' files.

The exit gate remains open in [Phase 03](../plans/03-deploy-and-recovery.md). Operator installation, readiness, and reported undo follow-ups remain in [Phase 06](../plans/06-release-and-ops.md).

## Resumed physical run, 2026-10-10: real v2-to-v3 migration

This run starts from `52977d3`, including imported-route provenance, owned container logs, preflight deadline reporting, and explicit terminal resolution. A new worktree was created from main after preserving the pushed earlier work. The current D3 standing test-host authorization was read from the repository. Application operations continue exclusively through the restricted client; operator access is used only for installation, read-only observation, backups and authorized fault injection. No tailnet, firewall or operator SSH configuration changed.

The current client and Linux arm64 executable were built from that commit. The previous host binary and a consistent read-only-source SQLite backup were preserved in a root-private drill backup directory. Installation used the same authorized binary-only replacement described earlier, not a supported upgrade command. The protected enrollment record was not rewritten and still has the stale enrolled binary hash; supported journal-aware updates remain P06-03 work.

Before replacement, read-only SQL observation of the actual control database reported schema 2. After installing the new executable, restricted operation status opened it and performed the production migration to schema 3. This was existing host history, not a synthesized migration fixture. The stuck removal receipt survived unchanged:

```text
operation 01a121b1e29e45390eb6945d6b142cabb2c2d61c6584
kind deploy; app fixture; state recovery_required
created 2026-10-09T17:24:28.446946677Z
updated 2026-10-09T17:24:33.295523865Z
events 13
before/after event-row SHA256 10a4342d6d17d74e4ac936dd81d93b5119e5e16238b205a24c53496aa9e66335
```

The event fingerprint hashes the ordered exact `seq`, `kind`, `state`, hex-encoded stored payload bytes and `created_at` rows. Original operation identity, plan association, state and timestamps matched before/after. `PRAGMA integrity_check` returned `ok` and `PRAGMA foreign_key_check` returned zero rows on both versions. No source events, runtime artifacts or release heads were manually edited. This proves real v2-to-v3 preservation; it does not retroactively claim physical v1-to-v2 evidence.

Local baseline gates passed all 30 Go packages, vet, client and Linux arm64 builds, empty formatting, diff check, and Darwin arm64 vet. No concurrency code changed in this resumed run. A local Quadlet test package took 329 seconds but completed successfully; no local gate failed or was bypassed.

### Supported resolution completes the original removal

```sh
"$SCRATCH/brine" resolve 01a121b1e29e45390eb6945d6b142cabb2c2d61c6584 \
  --target "$TARGET" --config-dir "$CLIENT_DIR" \
  --idempotency-key p03-original-removal-resolution --json
```

The restricted client accepted successor `01a122ef7112ec7a2a1182bfb1b08320f0dcfa4a87fc`, kind `resolve`, with `recovery_of` pointing at the original removal. It succeeded at `2026-10-09T23:11:22.834506305Z` (the local test session date is 2026-10-10). The original receipt remains terminal recovery-required, as designed; it was not reopened.

The successor's first `resolution` event links the source, followed by the adopted step payloads. Adoption stamps those copied events with successor-acceptance times; they do not retain the source timestamps. There is only one adopted withdrawal intent/completion pair and one adopted stop intent, with no additional withdrawal/stop intent during execution. The selected Caddy generation remains `gen-3`. Fresh stopped-writer inspection completed the previously unknown stop, then new `remove_unit`, `reload_units`, and `retire_app` intent/completion pairs finished under the successor. The actual `fixture.container` and selected route are absent, the user unit reports `LoadState=not-found`, and no Podman container or TCP listener remains on port 20000. Read-only SQL reports zero live release heads and one immutable removal receipt. Restricted `status` returns `apps:[]`.

History, the image, historical generation-2 route, and immutable secret v1 remain under D5; successful stateless removal is not a purge. No direct operator app mutation or DB editing was used to complete it. The secret's presence alone must not be mistaken for a committed app or a live port allocation. This resolves the earlier terminal-removal follow-up for that actual receipt; remove/recreate and the remaining clean-fixture drills are separate evidence.

### New blocker: retained-secret inventory prevents recreation

The resolved host has known control generation 2, zero release heads, no fixture Quadlet/container/listener, and one retained immutable secret. A plan using the original pinned image, route and secret reference was saved, but refused before apply:

```sh
"$SCRATCH/brine" plan "$SCRATCH/v1.toml" \
  --target "$TARGET" --config-dir "$CLIENT_DIR" --json
```

```json
{"schema_version":1,"command":"brine plan","ok":true,"data":{"plan_id":"sha256:b6c692e503484f9a702de2efa55939536ed01ce6744fc2e0b9c8417a64eaf62d","kind":"conflict","diff":null,"conflicts":[{"code":"unknown_facts","field":"app.image"},{"code":"unknown_facts","field":"app.port"},{"code":"unknown_facts","field":"apps.port"}]},"error":null}
```

No apply was accepted for this plan. This is not route provenance, missing registry metadata, an occupied port, an incomplete resolution, or failure to migrate. The completed removal has its durable retirement receipt; restricted status has no committed apps. Current Caddy generation 3 has no fixture route. The image still resolves successfully in connected planning.

A second read-only plan changed the desired name/domain to `fixture-two`/`fixture-two.brine.test` and removed all secret references. It returned plan `sha256:1e10c66a295c2d559ac75d4175a03753873f297ae7336bc7397304232fdc376d`, `kind:conflict`, with just `unknown_facts:apps.port`. No second app was deployed. Thus the existing secret-only inventory record can block port allocation even for another app; this is not a missing requested secret.

`internal/inventory/artifacts.go:109-115` initializes secret-only app image/port observations as unknown and only emits absence when global control generation is zero. After the successful removal, generation remains nonzero by design. `internal/plan/plan.go:312-324` refuses those facts for the requested app, and `:340-355` refuses an unknown port from any inventory app while allocating a new port. D5 intentionally keeps secrets/history after removal; deleting them or resetting generation is not the solution.

A temporary local regression adapted `TestReviewFirstDeploymentBindsPreexistingSecret` to a known generation 2, an empty Quadlet directory, matching generation-2 BrineState with no release heads, and the retained secret fixture. `go test ./internal/inventory -run '^TestRecreationRetainedSecretNonzeroGeneration$' -count=1` failed with exactly the three physical `unknown_facts` diagnostics above. The reproduction source/log remain in private task scratch; the failing test was not left in the committed tree. Production code was not loosened to manufacture absence. A safe solution needs affirmative per-app runtime/committed-state absence, retaining strict handling of missing artifacts, unowned containers/listeners, incomplete removal and unreadable state.

The lane stopped at that larger ownership/absence boundary as instructed. A new digest was resolved for the planned second release (`sha256:7377697a821c131a924a7105fafbe7414db4e9fcc77a6f08f776f33f141ec3f8`, arm64 manifest `sha256:e00b7e2763a0dfec9ec6d99253612510c253df47d7218cdd35c4e465b4e9ad1f`), but it was never applied. Update, stop-before-start, rollback, invalid release, parallel applies, client/SSH disconnect, run-op SIGKILL/dry-run preservation/reconciliation, new config/secret/lifecycle operations, nonempty app log tail, reboot and final clean-fixture removal remain owed. Both permitted reboots remain unused. Existing-data mounts, schema-breaking data rollback and R2 restore remain Phase 04; no zero-downtime claim is made.

Final state: no committed fixture app, unit, container, selected route or live port. Secret v1, image, historical route generations, original/recovery receipts and the root-private pre-upgrade binary/DB backup remain intentionally retained. Original terminal receipt/event hash still match the pre-migration observations after successor completion. No operator configuration, credentials, firewall, tailnet, policy, trust store or account was changed in this run.

Final local checks passed: `go test ./...` (all 30 packages), `go vet ./...`, `go build ./cmd/brine`, `gofmt -l ./cmd ./internal` (empty), `GOOS=darwin GOARCH=arm64 go vet ./...`, and `git diff --check`. This resumed PR changes evidence/planning only, not runtime or concurrency code. These local checks do not complete the remaining physical acceptance drills.

## Recreation resumed after #52, 2026-10-10

Merged `origin/main` (`1d44dc8`) into this lane without rebasing. Built client/Linux arm64 binaries and installed the host executable through the previously authorized binary-only replacement, preserving the prior executable privately. Protected enrollment provenance remains unchanged. The retained-secret blocker above is historical and is fixed by #52.

Recreation plan became `create` and operation `01a1236cd3356ea0b3b2c71ff7b8cfb80d47822b7b30` succeeded, binding retained secret v1. Direct HTTP and normally verified routed HTTPS both returned 200. Runtime/history were not purged. Default-deadline local tests timed out cumulatively in the progressing filesystem-effect suite (first at `remove/error/18-removed`, then at parent-sync checks; stacks showed `File.Sync`). A bounded 30-minute full-suite run with test fixtures on the data SSD is running; this is not evidence of a failed app operation.

### Environment and image update

Operation `01a1236d9d259f648697d9740960c4ceab49cf3cd42a` succeeded with `RELEASE=two` and the new pinned index `sha256:7377697a821c131a924a7105fafbe7414db4e9fcc77a6f08f776f33f141ec3f8`. Runtime inspection confirms the environment change and routed HTTPS returns 200. The journal orders completed `quiesce_old` before `install_unit`, `reload_units` and `start_unit`; deployment is stop-before-start, not zero downtime. Both direct/routed checks and commit completed.

### Physical rollback

The first rollback operation `01a1236f4e35118f6721b8497c95415092f6e96f73c9` failed `stale_plan` before any effect (launch plus failure/terminal events only). After inspecting that no-effect terminal receipt, a fresh rollback plan matched the equivalent original desired specification. Operation `01a123706a2ad1fcc644fac2d543f6a1711cc68438f2` succeeded through that supported plan/apply path. Runtime environment returned to `RELEASE=one`; normally verified routed HTTPS returned 200. This is stateless app rollback, not database rollback. The initial plan discrepancy is being inspected, not silently retried as an unknown operation.

### Invalid release and restricted log tail

Operation `01a1237124fb21710cca0733fe6fd9e6a06730d2cc40` attempted the new digest/environment with a deliberately nonexistent health path and settled `rolled_back`. The previous writer regained `RELEASE=one` and routed HTTPS 200. Restricted `logs fixture --tail 40` returned 22 nonempty entries after real HTTP requests; restricted `diagnose fixture` succeeded with the app report. No host journal privilege was granted.

### Config, immutable secret and lifecycle

Config set completed, then immutable secret operation `01a1237390c9f0e66c1c72616d98199d5817433b0ba1` stored v2 via stdin only. A later config plan bound v2 without overwriting v1; secret values are omitted from evidence. `stop`, `start` and `restart` all succeeded through restricted plan/apply (operations `01a1237457994ac67bb75ca67829656edcae3649457a`, `01a12374819623e0a9c4c771aafa02bbd09c247978c2`, `01a12374a9824e30a20d538ee03d7d16f3ac6c9e45d6`). Standalone app logs are nonempty, but diagnose app logs remain `unknown:probe_timeout`; unit logs remain `unknown:logs_journal_unavailable`. Diagnose tail acceptance is not claimed.

The full local suite passed all 30 packages with `GOFLAGS=-timeout=30m`, `TMPDIR`/`GOTMPDIR` on the data SSD. Default ten-minute suite runs had timed out in different progressing filesystem-effect subtests; the longer run completed rather than suppressing a test. Vet, Linux/client builds, Darwin arm64 vet, formatting and diff checks also passed.

### Diagnose tail defect fixed failing-first

On the healthy physical fixture, `logs` succeeded but `diagnose` consistently returned `unknown:probe_timeout` for the app tail. Diagnose injected a production log reader that recollected the full inventory inside a one-second tail probe, despite already having the report ownership snapshot under a larger budget. A regression observed three inventory collections and missing tails before the fix. Concrete injected app/unit log readers now use a copied reader bound to the already-measured report snapshot; custom reader adapters remain unchanged, and container/log-driver checks and redaction still run. No timeout or ownership rule was loosened. Full local checks passed. After binary-only installation, restricted `diagnose fixture` returned a known, nonempty 20-entry app tail. Unit journal unavailability remains an honestly reported separate fact, not a requirement for app logs.
