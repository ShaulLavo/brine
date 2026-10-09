# Phase 03 Pi run

Recorded 2026-10-09 against `9670c1a`, the merged Phase 03 production wiring, executor, reconciliation, config, secret, lifecycle, status, rollback, and diagnose implementation.

## Result

**Blocked before deployment. The Phase 03 exit gate has not passed.**

The current client and Linux arm64 host binary built successfully. Re-enrollment with the current binary reached confirmation, then refused the existing enrollment with `enrollment options differ from durable intent`. The installed binary still matches the root-owned enrollment record. Enrollment deliberately does not replace a previously recorded binary with a different one.

The enrolled target also lacks the current operator policy, trusted requester, forced-command authentication marker, and boot reconciliation artifacts. Replacing only the executable cannot supply those prerequisites. No documented manual upgrade procedure exists in the checked revision. This run stopped rather than rewriting the enrollment journal, undoing enrollment, or changing SSH policy outside a supported update procedure.

No fixture app was deployed. No binary was replaced, no reboot was issued, and no application mutation was attempted through operator SSH. Existing enrollment remains intact with no apps. No task checkbox changed. Controlled deployment downtime, recovery, and zero downtime are not demonstrated by this run.

## Evidence conventions

`$OPERATOR_TARGET` denotes the explicitly authorized operator SSH destination. `$TARGET` denotes the private client target name. `$CLIENT_DIR`, `$DEPLOY_PUBLIC_KEY`, and `$SCRATCH` denote private local paths. The published commands replace actual target aliases, account inventory, addresses, keys, and scratch paths with these variables. The fixed paths below are Brine's implementation paths, not a published host inventory.

Previous physical runtime evidence is in [the Pi runtime spike](../spikes/pi-runtime.md). There was no `docs/evidence` directory at the checked revision. The earlier spike does not prove that the current production deploy path works.

## Runtime and baseline

The authorized target reports Debian GNU/Linux 13, trixie, full version 13.6, on arm64. The installed Brine binary reports `0.1.1-dev`; the version string does not identify its source commit. The root-owned recorded binary hash matches the installed binary. This run did not attribute the installed binary to `9670c1a`.

The exact package revisions returned by `dpkg-query` were:

```text
caddy 2.6.2-12+deb13u1
passt 0.0~git20250503.587980c-2+deb13u1
podman 5.4.2+ds1-2+b2
systemd 257.13-1~deb13u1
```

Litestream was not queried. No backup or R2 restore was attempted.

Read-only operator inspection before and after the enrollment attempt returned the same prerequisite facts:

```json
{
  "binary_matches_record": true,
  "enrollment_phase": "enrolled",
  "binary_intent": true,
  "has_operator_policy": false,
  "has_requester": false,
  "has_boot_unit": false,
  "has_boot_generator": false,
  "control_state_files": []
}
```

The runner's external forced command selects `host serve` without the current `BRINE_AUTHENTICATED=deploy` marker. The current server factory requires that captured marker before constructing runtime services. The runner control-state directory is empty, so there is no v1 database to migrate. A clean v1-to-v2 migration on this physical target remains unverified. Creating a synthetic v1 database would not demonstrate preservation of existing host journals.

Read-only resource observations after the failed enrollment attempt returned:

```json
{
  "containers": {"exit": 0, "count": 0},
  "podman_secrets": {"exit": 0, "count": 0},
  "quadlet_count": 0,
  "caddy_route_count": 0
}
```

The rootless Podman observations ran from a readable working directory with the enrolled runner's HOME, runtime directory, and user bus. An initial observation from the operator's working directory failed; the corrected observations above succeeded. Neither observation mutated an app.

## Current builds

These builds completed from the isolated worktree:

```sh
export PATH=$HOME/.local/share/mise/shims:$PATH
go build -o "$SCRATCH/brine" ./cmd/brine
GOOS=linux GOARCH=arm64 go build -o "$SCRATCH/brine-arm64" ./cmd/brine
```

No current binary was installed on the target. No OCI fixture image was selected or pulled.

## Restricted-client observations

The current client used the existing private target configuration and restricted deploy-key transport. Operator SSH did not substitute for the client.

```sh
"$SCRATCH/brine" status --target "$TARGET" --config-dir "$CLIENT_DIR" --json
"$SCRATCH/brine" reconcile --dry-run --target "$TARGET" --config-dir "$CLIENT_DIR" --json
```

Both commands exited nonzero. Their captured JSON responses were:

```json
{"schema_version":1,"command":"brine status","ok":false,"data":null,"error":{"code":"dispatch_operation_refused","message":"The dispatcher operation is not allowed.","retryable":false}}
```

```json
{"schema_version":1,"command":"brine reconcile","ok":false,"data":null,"error":{"code":"dispatch_operation_refused","message":"The dispatcher operation is not allowed.","retryable":false}}
```

These are prerequisite refusals from the installed dispatcher, not evidence of a successful current-runtime status query or reconciliation preview.

## Supported enrollment attempt

The operator-only enrollment command used the new arm64 binary and the existing deploy key and client configuration:

```sh
"$SCRATCH/brine" enroll "$OPERATOR_TARGET" \
  --target-name "$TARGET" \
  --deploy-key "$DEPLOY_PUBLIC_KEY" \
  --host-binary "$SCRATCH/brine-arm64" \
  --config-dir "$CLIENT_DIR"
```

The client displayed the current fixed enrollment change list. After the target-name confirmation, the host refused before applying enrollment steps:

```text
enrollment options differ from durable intent
error: The operation failed.
operator SSH command failed; inspect and reconcile the host before retrying
error: The operation failed.
```

The command exited 1. The client removed its temporary uploaded enrollment executable. Post-attempt inspection confirmed the original enrolled phase, original binary match, empty control-state directory, and absent current-runtime prerequisites.

The refusal is at `internal/enroll/host_linux.go:359-360`. A recorded binary intent combined with a different source hash refuses before the call to `Apply`. [The enrollment contract](../CONTRACTS.md#operator-enrollment) explicitly says that a different binary or key is not an upgrade operation. The missing authentication marker is also material to `internal/host/factory.go:32-33`; the protected policy and requester are required by `internal/host/runtime_linux.go:74-77` and `:95-98`.

This is a missing operator installation path, not a request for mixed-version compatibility code. The next run needs a supported way to install the current enrollment artifacts together. A binary-only overwrite leaves recorded ownership stale and does not satisfy authentication or boot recovery.

## Acceptance still owed

| Evidence | Physical-host result in this run |
| --- | --- |
| T04 | Not run. No saved deployment plan or apply exists. |
| T07 | Not run. No digest-pinned release, immutable secret, direct health, or routed health result. |
| T08 | Not run. Invalid-release rollback or precise recovery-required outcome remains owed. |
| T09 | Not run. No detached deploy or real SSH-disconnect drill. |
| T10 | Not run. Concurrent applies and prompt acceptance during a running deploy remain owed. |
| T11 | Not run. No Caddy fault injection or drift recovery. Existing routes were not changed. |
| T16 | Not run. The requested app is stateless; schema-breaking data migration evidence belongs to Phase 04. |
| T17 | Not run on the physical target. No injection safety acceptance is claimed by these prerequisite refusals. |
| T21 | Not run. No effect-boundary kill, disk-pressure drill, or partial-write recovery. |
| T22 | Not run. No app removal occurred; archive, recreate, expiry, and purge evidence remains in later phases. |
| P03-03 | Real detached completion and unit collection remain owed. |
| P03-04 | Stateless staging, image verification, stop-before-start ordering, and routing remain owed. Existing-data mounts stay with P04-01. |
| P03-07 | Read-only preview preservation, effect-boundary reconciliation, and boot oneshot remain owed on the Pi. |
| P03-08 | Config, immutable secret rotation, and stop/start remain owed on the Pi. |

Both permitted reboots remain unused. The post-boot time to healthy is unmeasured. Local automated tests are not substitutes for these physical-host drills.

The installation follow-up is recorded under [P06-03](../plans/06-release-and-ops.md). The physical exit gate stays open in [Phase 03](../plans/03-deploy-and-recovery.md).

## Local verification

All local checks passed on the tested revision:

```sh
go test ./...
go vet ./...
go build ./cmd/brine
gofmt -l ./cmd ./internal
git diff --check
GOOS=darwin GOARCH=arm64 go vet ./...
```

`go test` passed all 30 packages. `gofmt -l` returned no files. The Linux arm64 cross-build also passed. Race tests were not rerun because this change modifies only documentation. No unit test or binary build is reported as a passing physical deployment.
