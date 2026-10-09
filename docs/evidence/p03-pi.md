# Phase 03 Pi run

Recorded 2026-10-09. The first attempt used `9670c1a`. The resumed attempt merged `c4caf3d`, including stateless removal, and tested the Docker Hub fix in `9e862a1` on the physical target.

## Result

**Blocked at routed planning. The Phase 03 exit gate has not passed.**

The first attempt stopped at the missing operator upgrade path. A separate operator session subsequently prepared a fresh enrollment. This lane verified the new restricted-client baseline, stored one fixture secret, reproduced and fixed Docker Hub image resolution, installed the fixed arm64 binary, and retried planning.

The image now resolves, but the plan reports `domain_owned`. The root Caddy configuration retains a hostless `:80` file-server site outside the Brine generation. That catch-all is not an owned fixture route. The lane did not delete it, change the operator policy, or relax ownership checks to bypass the conflict. No apply was launched.

The target has no deployed apps. The one newly created immutable fixture secret remains stored. `brine remove` was attempted and returned an `unowned_app` conflict because there is no committed fixture release. No operator-side secret deletion was attempted. No reboot was issued. No acceptance checkbox changed. Controlled deployment downtime and recovery are not demonstrated.

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

The index contains linux/amd64 and linux/arm64 entries. The TOML used only the immutable index, container port 8080, `fixture.brine.test`, a 30-second startup deadline, HTTP `/` with status 200, an environment release marker, and the allowed `fixture-token` reference. No image pull or container startup occurred.

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

This is a routing precondition, not a reason to silently remove an operator site. A usable non-public fixture also needs a trusted local HTTPS certificate because routed health uses normal TLS verification. This run did not establish that certificate prerequisite or attempt issuance. Readiness work under P06-02 records both prerequisites.

## Removal and remaining resources

The real client attempted cleanup planning:

```sh
"$SCRATCH/brine" remove fixture --target "$TARGET" --config-dir "$CLIENT_DIR" --json
```

```json
{"schema_version":1,"command":"brine remove","ok":true,"data":{"secret_retention":"d5_retained_releases","lifecycle":"remove_app","plan_id":"sha256:04b7813052b96d564b1b3345f7aa4b70592d66208170d1aad978efc671512d9b","kind":"conflict","diff":null,"conflicts":[{"code":"artifact_drift","field":"unowned_app"}]},"error":null}
```

No removal apply was launched for this conflicting plan. Final app status returned `apps:[]`. The fixture secret `brine.fixture.fixture-token.v1` remains stored without a release binding. Its retention and explicit operator cleanup are still owed. Do not interpret the empty app list as proof that there are no fixture-created secrets.

## Acceptance still owed

T04, T07-T11, T16-T17, T21-T22 were not completed on the physical host. No accepted apply exists. Direct/routed health, release replacement, stop-before-start ordering, rollback, invalid-release recovery, concurrent acceptance, disconnect, effect-boundary kill/reconcile, boot recovery, config update, secret rotation, stop/start, and successful stateless removal remain owed.

The immutable secret assignment and image-resolution planning above are only partial P03-08 and P03-04 evidence. The `remove` conflict is not P03-09 physical acceptance. Persistent-data mounts and schema-breaking migration drills remain with Phase 04. Both permitted reboots remain unused. No acceptance checkbox changed.

## Local verification

Before pushing the Docker Hub fix, all 30 packages passed `go test ./...`. `go vet ./...`, the client build, Linux arm64 cross-build, empty `gofmt -l ./cmd ./internal`, `git diff --check`, and `GOOS=darwin GOARCH=arm64 go vet ./...` passed. The Docker-specific regressions failed before the fix and passed afterward.

These checks do not establish a working physical deployment. The exit gate remains open in [Phase 03](../plans/03-deploy-and-recovery.md). Operator installation, readiness, and undo follow-ups are recorded in [Phase 06](../plans/06-release-and-ops.md).
