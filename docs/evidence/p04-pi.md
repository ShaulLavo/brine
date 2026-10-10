# Phase 04 physical acceptance on the test Pi

## Scope and result

This run uses D3's standing approval for the disposable Debian 13 arm64 test host. It starts from `32926d8a7d0a8d54e329f2ef671abe539d1cfb1d`, the P04-01 completion branch stacked on the connected replication branch. Operator SSH, tailnet and firewall configuration remain unchanged.

**The persistent exit gate is not met.** Preflight found missing connected restore and credential-rotation paths and no available reviewed empty-initialization workflow. The coordinator corrected the root-ownership instruction and supplied operator-attested lifecycle facts. This checkpoint is limited to real control-database migration and history preservation. No persistent application, replica or remote restore is claimed. P04-01 through P04-06 remain unchecked.

The destination is recorded here only as `<endpoint>/<bucket>`. Credential values, provider account identifiers, client configuration, host inventory and raw database contents are excluded. The scoped credential packet was neither printed nor copied into the repository.

## Operator backups

Before changing the executable or policy, the operator preserved the existing executable and policy and made a consistent SQLite backup through Python's SQLite backup API. The source connection used `mode=ro`. The backup directory is root-owned 0700 and the database backup is root-owned 0600. The policy and its preserved copy have identical SHA-256 hashes. No control rows were manually edited.

The real control database began at schema 3 in its `schema_version` table. `PRAGMA user_version` is 0 and is not Brine's migration version.

| Historical table | Rows before migration | Ordered-row SHA-256 |
| --- | ---: | --- |
| operations | 25 | `b58cc1b903a78f8ec481e3c02ffd16c5c76267d20fecb11829ac63d596d91f17` |
| events | 569 | `8ba744d8b5ab8185c24598c8d75410cebb2085a2067e1784919640caf23b0827` |
| releases | 9 | `c436b4c5e6599cc5b6603ea4b2fabd987d00a43be5046cc8565e3d4152700484` |
| plans | 24 | `e21fecf604110fa6986bf966037ccc77512b77a6ade8e6fffbc9c4c5a55e9ccf` |
| transitions | 84 | `f08649ca8871c897f35830eddaf753a273419b9d91693b236ce4741330dc26c6` |
| release_heads | 0 | `4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945` |
| app_removals | 2 | `ff4f32d2817f069cbf04e29859dd748e80eaf5877e6b71c2463274a544188b8c` |

Each fingerprint hashes `json.dumps(rows, sort_keys=True)` from `SELECT * FROM <fixed table> ORDER BY 1`, with SQLite BLOB values encoded as lowercase hexadecimal. Direct before/after row equality, not a row count alone, is the preservation check.

## Completed control-database migration

After merging the moving P04-01 and connected-startup parents without rebasing, the tested source was `c8606fd72215c5395cae2a61bef9c1363850fd0c`. The Linux arm64 executable's SHA-256 was `97fe272b51a133d47b1d775e3406fc351d2ffe516e667bbdf8f8bb3b71a3dd54`; the installed executable matched it. Local gates passed before installation.

The operator used the same authorized binary-only replacement as Phase 03: copy the built executable to a temporary host path, verify the existing executable against its preserved backup, then install the new executable as root-owned 0755. The protected enrollment record was not rewritten; its enrolled executable hash remains stale. This is not evidence of a supported journal-aware upgrade or undo path.

Restricted operation status opened the real runtime store and performed the production migration from schema 3 to schema 5. The existing final removal receipt still reported `succeeded`, with 21 events and next cursor 21. No SQL migration was manually applied and no historical row was edited.

A read-only comparison of the preserved schema-3 backup and live schema-5 database asserted exact equality of all seven historical tables above: **713 rows remained unchanged**, including their ordered-row hashes. The live database returned `ok` from `PRAGMA integrity_check` and zero rows from `PRAGMA foreign_key_check`. Restricted app status reported no committed apps.

After migration, operator SSH still worked and the protected policy remained byte-identical to its backup. The preserved executable, policy and consistent schema-3 database backup remain in the root-private drill backup directory, with directory mode 0700 and database mode 0600. No persistent application, replica, policy entry, credential, route, access setting or reboot was changed.

The protected Litestream executable already exists and reports `0.5.17`. No tool installation was needed. The host reported `running` through `systemctl is-system-running`; the process-name check found no package-update process. These observations do not prove that a later reboot is safe. Check again immediately before rebooting.

## Blocking prerequisites

### Connected credential activation and restore

`internal/cli/backupcredentials.go` exposes credential plan and set. Its human responses explicitly say that activation is not implemented and that replication was not restarted or activated. `internal/host/backup_credentials.go` composes private files, journal, lock and scope checking but has no replica activation hook. Installing v2 therefore cannot prove drill 9's replica-only restart.

At this source commit, there is no `internal/restore` package and `internal/cli/root.go` registers no `restore` command. The restore engine is separately under review in #64. A connected-operations lane is adding the command, a database selector and credential-rotation activation. Drill 6 cannot run through the requested connected `restore test` path until those changes land. A manually invoked Litestream restore would prove a different operation and would not substitute for that acceptance.

### Corrected persistent root setup

The initial brief requested a root-owned policy entry. `internal/data/root_linux.go:32` requires the selected root itself to be runner-owned 0700. `ProbeRoot` and `ProbeDataMapping` create their disposable probe directories directly under that root.

The coordinator corrected the brief. A later deployment will create the selected root runner-owned 0700 under a root-owned parent, following D11 and the implementation. This is no longer a design blocker. No root or policy entry was created during this migration-only checkpoint.

### Reviewed initialization and destination lifecycle

D12 and the schema-migration contract require `startup = "preserve"`. Initial table creation is a separate reviewed schema change with a verified empty restore point. No initialization operation exists on this stack. A new initialization lane is implementing that workflow. A fixture that silently creates `brine_schema_marker` at startup would violate that declaration, even on the first release. The empty allocation receipt proves absence; it does not authorize schema creation.

The planner also requires fresh protected `backup_retention` evidence bound to the exact destination, with `no_object_expiration = true`. Prefix-scoped data credentials cannot establish bucket lifecycle policy.

At `2026-10-10T11:31:09Z`, the coordinator captured the provider lifecycle rules as operator and attested that the destination has no object-expiration rule. Its sole rule aborts incomplete multipart uploads after seven days; it does not expire committed backup objects. The raw provider JSON remains private. A resumed deployment can encode this attestation using the existing `backup_retention` policy fields in `internal/data/admission.go`. No policy receipt was installed in this migration-only checkpoint. The attestation is not evidence of a successful upload or restore.

### Fixture publishing

The available GitHub authentication reports `gist`, `read:org`, `repo` and `workflow` scopes, without package-publishing scope. The coordinator is asking the owner to add `write:packages`. No alternate authentication path was attempted, no registry credential was changed or added, and no image was pushed.

## Drill ledger

| Step | Result | Evidence still required |
| --- | --- | --- |
| 1. Credential plan/set | Not run | Allocate admitted binding, plan scoped delivery and pipe the private packet into set. |
| 2. Persistent fixture | Not run | Reviewed initialization, published multi-architecture digest, admitted root and runner-owned 0600 DB/WAL/SHM plus 0700 metadata. |
| 3. Remote writes | Not run | Independently observe known writes in real storage within the configured interval. |
| 4. Replacement | Not run | New digest, completed old-writer stop before start, preserved sentinel. |
| 5. Reboot | Not run | Fresh update-process check, boot permits and intact data after reconnect. |
| 6. Isolated restore | Blocked | Connected restore command, integrity, foreign keys, schema, sentinel, honest loss window and unchanged live bytes. |
| 7. Interruptions and fence | Not run | Actual prepare/start kills with reconciliation and fail-closed app/replica starts. |
| 8. Failed update | Not run | Compensation starts the committed previous unit without changing data. |
| 9. Credential rotation | Blocked | Fresh externally minted packet and connected replica-only activation. |

No outage, interruption, credential expiry, competing replicator, loss bound or successful restore is inferred from unit tests. Asynchronous replication cannot guarantee zero loss of last writes. During upload failure or without an independently verified remote watermark, the loss window is unknown.

## Local verification

The client and Linux arm64 host executable are built through the shared heavy-job queue with `GOCACHE=/work/cache/go-build`. The full suite uses the prescribed serialized, bounded gate for this loaded host.

The first queued attempt used the original worker rule's `--quiet` and was stopped by the scheduler's 600-second quiet-hold limit with exit 75. It had passed through the spec package, including the 312.781-second Quadlet package, but had not finished the full suite or produced the binaries. This was not a passing gate. The coordinator then directed cancellation of the queued quiet retry and required ordinary, nonexclusive scheduling for tests and builds. No quiet job remains active for this lane.

Before cancellation, the retry reported `TestDataPreparationPublishesCommitsActivatesAndProvesExactArtifacts` failing at `internal/host/persistent_linux_test.go:113` with `replication: invalid binding or configuration`. The fixture creates its state root with `MkdirTemp` at line 39. This worktree's original temporary path is 39 bytes long. A nine-digit generated basename produces a 107-byte control socket; a ten-digit basename produces 108 bytes. `internal/replication/config.go:64` correctly refuses socket paths over 107 bytes. This explains the intermittent fixture refusal without changing production limits.

The coordinator assigned the fixture fix to its owner and explicitly allowed a shorter temporary root for this lane. The final run uses an independently created, private `/work/tmp/p4d-XXXXXX` directory for `TMPDIR` and `GOTMPDIR`, and the `suite` queue class without `--quiet`. No test or production deadline was changed. The fixture's production path bound remains unchanged.

```sh
go test -p 1 -timeout 30m ./...
go vet ./...
go build -o .tmp/brine ./cmd/brine
GOOS=linux GOARCH=arm64 go build -o .tmp/brine-arm64 ./cmd/brine
test -z "$(gofmt -l ./cmd ./internal)"
git diff --check
```

All commands above passed on the merged source; the shared queue job exited 0. The full suite included host (57.757 seconds), Quadlet (242.233 seconds) and store (58.106 seconds) tests. Both client and Linux arm64 builds completed, vet passed, gofmt output was empty and the diff check passed. Earlier interrupted or failed attempts are not counted as successful verification.

This drill adds evidence only, not new production or concurrency changes. The parent merges contain their owners' implementation changes. No race run was added in this evidence lane. Physical persistent-app drills remain unrun.

## Resume gate

Resume after the reviewed initialization implementation, restore engine and connected restore/rotation implementation land, and fixture publishing is authorized. Review the initialization procedure against D12 before running it. Create the root with the corrected runner ownership. Encode the operator lifecycle attestation in protected policy and recheck its freshness.

Recheck the live policy, control state and package-update state rather than assuming this checkpoint is still current. Preserve the existing operator backups and history. Continue with the same private destination and credential scope unless the coordinator supplies a replacement. Credential rotation still requires a newly minted packet from the coordinator.
