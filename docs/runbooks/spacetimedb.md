# Operate a managed SpacetimeDB app

Phase 09 is Approved and incomplete. These commands describe the implemented
operator flow. The [physical evidence](../evidence/p09-pi.md) records first
publication, Pi browser writes and ordinary online capture with real R2 replay
on both architectures. Module updates, live restore, archive, assigned fault
cases and final delivery remain incomplete. Keep the
[phase tasks](../plans/09-spacetimedb.md) unchecked until their full evidence exists.

Use an enrolled target and its restricted personal identity. Replace uppercase
arguments with declared names or IDs returned by Brine. Save each reviewed plan,
operation ID and reported idempotency key in a private report directory. A
successful machine envelope for submission can mean only `accepted`. Poll the
operation before taking a dependent step:

```sh
brine status --operation OPERATION_ID --target TARGET --json
```

If enrollment uses a custom target directory, add `--config-dir PRIVATE_CONFIG`
to every target command below. `ARTIFACTS` names the retained fixture build's
artifact directory; use its exact pinned Wasm files.

Proceed only after the required operation succeeds with its matching receipt.
`--no-wait` does not verify an effect. A lost response or `recovery_required`
outcome requires the inspection flow below.

## Obtain a matched CLI build

Go CI publishes Linux amd64 and arm64 binaries in `brine-cli-SOURCE_HEAD`,
retained for three days. Pull request artifacts use the actual reviewed branch
head. The artifact includes `receipt.json`, compiler metadata and CLI version.
Compare its source head, architecture and SHA-256 with the downloaded binary
before using it. The complete CI gate and actual host drill establish acceptance.

To reproduce the pair on a Linux amd64 host, run this from a clean checkout with
an unused output directory outside the repository:

```sh
bash scripts/build-cli-pair.sh OUTPUT_DIRECTORY
```

## Check prerequisites

1. Obtain operator authorization for the target, disposable app and destination.
   Enroll through the [existing enrollment flow](../CONTRACTS.md#operator-enrollment).
   Keep the target configuration and private input files outside the repository.
2. Select the pinned engine from
   [selected native engine](../evidence/p09-native-engine.md). Enrollment installs the
   passive reader's [architecture-specific artifact pins](../../internal/enroll/native_reader.go).
   Select the immutable
   frontend image and matching Wasm/client artifacts from the
   [fixture publication](../../fixtures/spacetimedb-app/README.md#pinned-build-and-publication).
   Permit `ghcr.io` with the `shaullavo/brine-native-engine` repository in the
   protected registry policy. Admit only the licensed single-instance topology.
3. Declare `engine = "spacetimedb"` under `[[databases]]`. Include the app-owned
   root, nested service, module artifacts, schema compatibility, resource bounds,
   backup destination and separate secret references. Use the
   [managed declaration contract](../CONTRACTS.md#managed-spacetimedb-deployment-and-lifecycle-p09-04).
   Keep declared Wasm and schema sidecars inside the spec directory without
   symlink components. Sidecars describe input; protected staging still verifies
   the actual module and schema before publication.
4. Have the operator review the protected app, root, destination, secret and
   SpacetimeDB policy scopes. Review capture cadence, verification age, retention,
   possible-loss budget and exact-operation maintenance allowance. Live restore
   requires the separate default-false `allow_live_restore` grant. Early local
   archive purge requires the separate default-false `allow_archive_purge` grant.
5. Check the actual target mount, free space and available memory before pulling
   images or staging captures. Allow space for encrypted transfer, authenticated
   download, private working copy and retained original trees. Fixture
   measurements do not establish production capacity.
6. Record pre-existing resources and install teardown before the drill. Name
   every new process, unit, container, image, route, tree and download. Preserve
   unrelated services and retained recovery dependencies during cleanup.

Validate desired inputs locally, then inspect actual target dependencies:

```sh
brine validate brine.toml --policy operator-policy.toml --json
brine diagnose --target TARGET --json
```

Validation does not start an engine, publish a module or verify remote recovery.
Managed ingress requires the qualified Caddy version and logging policy that
exclude query tokens. Public client routes must deny native management, arbitrary
SQL and publication access. Keep the publisher token out of browser assets and
browser input.

### Admit protected workspace observation

Workspace teardown must prove that no process retains the private restore
directory or its members. Systemd can keep descriptors through FDSTORE after
the sender exits, so a manager role or stopped sender is insufficient.

The observer requires the separate protected file
`/etc/ssh/brine/workspace-observer.toml` with an explicit operator grant:

```toml
allow_workspace_observation = true
```

Fresh enrollment records the actual runner UID/GID before installing and starting
`brine-workspace-observer.service`. It creates the protected grant with false
permission when absent and preserves a valid existing operator-edited grant.
The listener can start during enrollment; requests remain denied until the
protected enrollment phase is exactly `enrolled`.

Missing, omitted or false permission denies observation. This file does not
alter the deployment operator-policy hash. Scope comes from the current
protected policy's persistent roots and enrolled runner identity. The private
root service observes one actual runner-owned `0700` directory descriptor under
`native-restores/ID`, where `ID` is lowercase 32-character hex. Callers cannot
supply a path, PID, command or absence proof. The service has only read-only
process-consumer observation authority, with no signal, execution or deletion
action.

If the service is unavailable, a consumer remains or observation is unknown,
retain the workspace and report recovery-required. Observation does not release
a data fence. Require qualification of the installed service before relying on
its absence result. A real FDSTORE control must refuse while the manager retains a workspace
descriptor after sender exit, then permit absence after that descriptor closes.
Record the installed sandbox and scoped grant with that result.

## Allocate and deliver material

Save the original connected preparation plan and apply it once:

```sh
brine plan brine.toml --target TARGET --json
brine apply PREPARATION_PLAN_ID --target TARGET --json
brine status --operation PREPARATION_OPERATION_ID --target TARGET --json
```

Require a successful unpublished allocation receipt. Engine, frontend and
ingress remain stopped, and the original data fence remains held. Deliver the
[four-role private packet](../native-material-commands.md) through stdin:

```sh
brine data material set APP --preparation-plan PREPARATION_PLAN_ID \
	--target TARGET --json < private-material.json
```

Require `configured_unpublished`. The supplied publisher token needs an explicit
future UTC lease, a matching P256 signing pair and the qualified
`supplied_key_jwt` policy with `require_client_token=true`. Generic secret delivery
does not establish these native roles. External OIDC is not qualified.

Keep `PREPARATION_PLAN_ID`. Recovery delivery, backup policy and first publication
reuse this exact original allocation plan. After material delivery, ordinary
planning reports `managed_publication_required` with no executable changes.
Do not reapply the original plan or substitute that new ordinary plan.

Deliver the independent recovery password and externally issued scoped S3
credentials, then activate backups:

```sh
brine backup recovery set --deployment-plan PREPARATION_PLAN_ID \
	--target TARGET --json < recovery.json
brine backup credentials plan APP --database NAME --expires-at ISSUER_EXPIRY \
	--target TARGET --json
brine backup credentials set APP --database NAME --plan-id CREDENTIAL_PLAN_ID \
	--target TARGET --json < private-credentials.json
brine backup policy plan --deployment-plan PREPARATION_PLAN_ID --target TARGET --json
brine backup policy activate BACKUP_PLAN_ID --target TARGET --json
```

Use the real issuer expiry in RFC3339 UTC ending in `Z`. The credential packet
contains `access_key_id`, `secret_access_key`, optional `session_token` and optional
matching `expires_at`. The recovery packet is exactly `{"password":"..."}` with
16 to 4096 bytes and no NUL. Keep an independently recoverable copy of that
password. Clear disposable private input copies after their operations settle.

The encrypted archive includes the selected signing private key. Publisher
tokens, S3 credentials and the recovery password stay outside it. Preserve every
immutable signing and recovery version needed by a retained point or archive.
Brine does not mint or extend issuer credentials.

## Publish the first module and deploy

Activate backups before preparing the exact empty allocation:

```sh
brine backup prepare APP --database NAME --target TARGET --json
brine data module plan APP --point PREPARED_POINT_ID \
	--deployment-plan PREPARATION_PLAN_ID --wasm ARTIFACTS/module-v1.wasm --target TARGET --json
brine data module apply APP MODULE_PLAN_ID --target TARGET --json
brine plan brine.toml --target TARGET --json
brine apply DEPLOYMENT_PLAN_ID --target TARGET --json
brine status APP --target TARGET --json
```

Require a `native_prepare` receipt with `outcome: "verified"` before publication.
Its point, binding and epoch must match the original allocation. Empty module and
schema hashes are valid only for an affirmatively unpublished source. Require
`module_publish_apply` with `outcome: "published"`, `transition_state: "completed"`
and a distinct `new_epoch_id` before planning deployment. Preparation retains its
fence and measured maintenance time. Publication privately verifies actual ownership, module, schema and initial
data, preserves the original tree, and activates a new epoch. It leaves services
stopped. Only the fresh ordinary deployment plan starts the compatible service
and frontend. App startup never creates or publishes a database.

Use the fixture's [private drill client](../../fixtures/spacetimedb-app/README.md#explicit-private-drill-client)
to authorize a bounded writer. Connect the matching browser client through the
selected database's HTTPS/WebSocket routes with an ordinary user token. Record
explicitly confirmed paired rows and the exact expected sequence. A reducer
acknowledgement, health response or unconfirmed subscription is insufficient.
Test management-route denial, forged/expired tokens and unauthorized writes.

## Capture online and test recovery

Keep source writers running for the online capture:

```sh
brine backup capture APP --database NAME --target TARGET --json
brine backup status APP --database NAME --target TARGET --json
brine restore native-test VERIFIED_POINT_ID --target TARGET --json
```

Require `outcome: "verified"` from the matching capture and restore-test receipts.
Compare binding, epoch, point, module and schema identities with the expected
confirmed data. The capture receipt has kind `native_capture`. The test receipt
has kind `native_restore_test`. The test uses authenticated remote extraction and isolated
passive replay, then disposes its reader without changing live data. SQLite uses
`restore test` and Litestream; its TXID or receipt cannot select a native point.

Record `capture_interval_seconds`, `maintenance_millis` and `loss`. Online capture
and restore-test report zero maintenance. Loss remains `unknown`; cadence, upload
success and a verified lower bound do not prove zero loss. Status reports missed
intervals, verification age, credential expiry, pending failures and held fences.
Status reads protected records without contacting storage or refreshing proof.

## Abandon an unused exact preparation

If a successful initialized preparation will not be used, abandon its recorded
point through the restricted target. Expiry alone leaves the original fence held.
A fresh or expired unused point can be abandoned, but a consumed point,
unresolved effect, changed source or incompatible current scope refuses.

```sh
brine backup abandon PREPARED_POINT_ID --target TARGET --json
brine status --operation ABANDON_OPERATION_ID --target TARGET --json
```

Require a successful `native_prepare_abandon` receipt with `outcome: "abandoned"`
and the original point, binding, epoch and preparation operation. Brine rechecks
the checkpoint, owned source, compatible current code, stopped units, closed
routes and current policy under retained locks. Completion releases only the
original fence and records a stopped desired revision. Engine, frontend and
ingress stay stopped. Point expiry, source data, the original journal and remote
backups remain unchanged.

After successful abandonment, use ordinary lifecycle planning to resume the
compatible app. Prepare a distinct fresh point before another reviewed data
mutation. Abandonment never extends expired coverage or authorizes publication.
For a lost response, inspect the original operation and use its retained key.
Do not clear its fence or infer success from an unjoined receipt.

The [physical evidence](../evidence/p09-pi.md) records source52 installation
and a public abandonment refusal before effects. The detached-requester fix is
locally verified but physically unrun. Successful abandonment and ordinary
resume remain required.

## Review a module update

Review init, update, lifecycle and scheduled-reducer effects with the exact Wasm
and compatible retained recovery release. First update the desired declaration
in `brine.toml` to the candidate Wasm and canonical schema sidecars. Select the
matching application client and compatible recovery release. The review checks
the extracted candidate descriptor against that saved target declaration. Then
prepare fresh current coverage:

```sh
brine plan brine.toml --target TARGET --json
brine backup prepare APP --database NAME --target TARGET --json
brine data module plan APP --point PREPARED_POINT_ID \
	--deployment-plan TARGET_PLAN_ID --wasm ARTIFACTS/module-additive.wasm \
	--recovery-release COMPATIBLE_RELEASE_ID --target TARGET --json
brine data module apply APP MODULE_PLAN_ID --target TARGET --json
brine plan brine.toml --target TARGET --json
brine apply DEPLOYMENT_PLAN_ID --target TARGET --json
```

Use the retained target declaration from the first command. Review expires after
at most fifteen minutes and binds the authenticated requester, exact artifact,
source, roles and recovery releases. An ordinary online point cannot substitute
for exact preparation. Omit `--recovery-release` only for an unpublished
incarnation with no committed release history.

Require preserved confirmed data and the expected new schema before restarting.
Pair a default-column module with its regenerated client image. Reject
destructive or access-changing candidates through review; native `AutoMigrate`
flags do not authorize them. No raw publish, `--delete-data`, `on-conflict` or
unrestricted `--yes` substitutes for the reviewed operation.

## Separate code rollback from data restore

To change application code, review a stored release against current data:

```sh
brine rollback APP --release COMPATIBLE_RELEASE_ID --target TARGET --json
brine apply ROLLBACK_PLAN_ID --target TARGET --json
```

Code rollback preserves current data and epoch. Incompatible code must conflict.
To replace data, obtain the live-restore policy grant and review possible lost
writes. Preserve fresh exact current coverage and its recovery dependencies:

```sh
brine backup prepare APP --database NAME --target TARGET --json
brine restore native-live plan APP --database NAME --point HISTORICAL_POINT_ID \
	--prepared-point CURRENT_PREPARED_POINT_ID --recovery-release COMPATIBLE_RELEASE_ID \
	--target TARGET --json
brine restore native-live apply APP --plan RESTORE_PLAN_ID --target TARGET --json
brine plan brine.toml --target TARGET --json
brine apply DEPLOYMENT_PLAN_ID --target TARGET --json
```

Restore preserves the current original tree, verifies historical data privately,
installs the reviewed compatible code and activates a new stopped epoch.
Replan deployment against that epoch. A failed app start never authorizes an
automatic reverse data swap.

## Recover an interrupted operation

Read the original operation status first. Keep its fence, private stages, journal,
original trees and recovery versions while inspecting the actual effect.

For interrupted capture, preparation or backup-policy activation, inspect the
original operation:

```sh
brine backup recovery inspect ORIGINAL_OPERATION_ID --target TARGET --json
brine backup recovery review ORIGINAL_OPERATION_ID --target TARGET --json
brine backup recovery resolve REVIEW_ID --target TARGET --json
```

Proceed to review when inspection returns `state: "verified_remote"` or the
qualified preparation state `stopped_unpublished`, with the expected original
operation, binding, epoch and observation hash. For backup activation, inspect
also reports the actual `units_state`. Resolve selects the returned review ID.
Use the review within ten minutes. Require `native_backup_resolve` with
`outcome: "adopted"` and its matching nested original receipt. The original task
outcome remains unchanged.

Resolution rereads actual remote and local state. It never repeats an issued
upload or repository initialization. Require both successful task completion and
verified cleanup before using its completed record.

For an unpublished preparation stopped before capture or upload, inspection can
return `state: "stopped_unpublished"` and `units_state: "absent"`. Brine must prove
the original stop, fence, configuration and material, an unchanged empty source,
settled initial-helper attempts, and absence of consumers throughout the staging
parent and source. Missing journal rows alone cannot prove absence. Reviewed
resolution continues that original preparation and PointID, captures only
unissued work, and returns its nested `native_prepare` receipt. Keep the original
fence and operation if inspection refuses. Do not submit another preparation key
to bypass the refusal.

Cleanup proves absence through the protected observer, then records an owned
retirement intent before deleting that exact workspace. After a crash, a missing
directory is adopted only with its matching retained intent and descriptor
identity. Missing without an issued intent, a replacement tree or unknown
consumers refuses. Inspection and review also report their stage-cleanup failures;
a verified remote read cannot hide a failed cleanup or return a success view.

For module publication:

```sh
brine data module inspect MODULE_PLAN_ID --target TARGET --json
brine data module resolve APP MODULE_PLAN_ID --operation ORIGINAL_OPERATION_ID \
	--target TARGET --json
```

Resolution adopts proven state without repeating publication, private stop or
repository initialization. A source-preserved result records stopped intent and
does not assert that an unknown private publication never happened.

For live restore, use the [restore inspection and reviewed resolution flow](../native-restore-recovery.md).
For a lost native-material response, use [material status by operation or saved key](../native-material-commands.md).
Generic `reconcile` and `resolve` do not admit these native flows. If evidence
conflicts or consumers remain, preserve the refusal and operation ID for
target-specific operator recovery. Plan or point expiry does not release a fence.

## Archive and retain the app

Prepare fresh exact current coverage before reviewing whole-binding retirement:

```sh
brine backup prepare APP --database NAME --target TARGET --json
brine archive native plan APP --target TARGET --json
brine archive native apply APP --plan ARCHIVE_PLAN_ID --target TARGET --json
brine archive native inspect ARCHIVE_ID --target TARGET --json
```

Save both `id` and `archive_id` from the archive plan. Apply selects its plan ID;
inspect and status select the archive ID. Resolve selects the original operation
ID. These selectors are not interchangeable.

Retirement preserves current data, prior terminal transition trees, all remote
epochs and recovery versions. Thirty-day retention starts after all moves
complete. Recreating the app requires new identities and destination prefixes.
For an interrupted retirement with an existing manifest:

```sh
brine archive native resolve ORIGINAL_OPERATION_ID --target TARGET --json
```

Uncertain preparation without a manifest requires operator inspection with the
original fence held. Inspect retained credential and verification health:

```sh
brine archive status ARCHIVE_ID --target TARGET --json
```

To renew externally issued credentials, select the original archived binding
and epoch, not a recreated app name:

```sh
brine archive credentials plan ARCHIVE_ID --binding BINDING_ID --epoch EPOCH_ID \
	--expires-at ISSUER_EXPIRY --target TARGET --json
brine archive credentials set ARCHIVE_ID --binding BINDING_ID --epoch EPOCH_ID \
	--plan ARCHIVE_CREDENTIAL_PLAN_ID --target TARGET --json < private-credentials.json
```

Use the bounded private S3 packet and matching issuer expiry described above.
Renewal activates no replica or engine and does not extend the original deadline.
Run fresh remote retention verification under that retained scope:

```sh
brine archive maintain plan ARCHIVE_ID --kind verify_retention --target TARGET --json
brine archive maintain apply ARCHIVE_ID --plan RETENTION_PLAN_ID --target TARGET --json
```

Require a matching receipt with `outcome: "verified"`, `evidence_sha256` and
`completed_at`. Status preserves the last verification time without probing the
provider. Failed verification does not refresh it.

After the original thirty-day deadline, review local expiry:

```sh
brine archive maintain plan ARCHIVE_ID --kind expire_archive --target TARGET --json
brine archive maintain apply ARCHIVE_ID --plan EXPIRY_PLAN_ID --target TARGET --json
```

Deliberate early local deletion instead requires `--kind purge_archive`, the
separate `allow_archive_purge=true` operator grant and fresh independent retention
proof. Review and execution recheck those conditions. Require `local_deleted`;
remote epochs and keys remain retained. Neither operation deletes provider objects.

For an interrupted journaled local deletion, resolve its original operation:

```sh
brine archive maintain resolve ORIGINAL_OPERATION_ID --target TARGET --json
```

The resolver derives the exact archive and plan from that operation. It adopts
known original effects and finishes only that admitted deletion scope. It keeps
the original outcome unchanged and returns its own matching receipt.
Do not supply a replacement plan or use native
retirement resolution for maintenance. Apply and resolve support
`--idempotency-key` and `--no-wait`; acceptance still requires later status.
Physical renewal, retention, purge/expiry and interruption acceptance remains
pending.

## Finish the drill and requalify upgrades

Keep scrubbed commands, receipts, immutable artifact pins, confirmed marker
comparisons and resource measurements in the physical evidence report. Verify
teardown on the actual execution host. Remove task-owned readers, staging,
transferred binaries, browser profiles, test images and routes after their
consumers stop. Report retained archives and original trees with size, owner and
removal condition. Never use blanket host pruning.

Use the existing pinned version until the
[runtime and physical exit gates](../plans/09-spacetimedb.md#exit-gate) pass for
an upgrade. HA, arbitrary point-in-time recovery and unqualified live-copy states
remain unsupported. The [acceptance matrix](../TEST_MATRIX.md#phase-09-operator-command-and-evidence-matrix)
lists the remaining evidence; this runbook does not complete it.
