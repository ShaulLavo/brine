# Native backup command contract

The commands use the existing detached operation engine and protected native
backup records. Capture reads the running source, encrypts its selected native
history and signing recovery material, uploads it, then independently restores
and verifies it. Preparation withdraws owned ingress and proves a clean stop
before making exact coverage. Both retain unknown effects for inspection.

The physical evidence records ordinary online capture and
same-point real R2 native tests on arm64 and amd64, plus a Pi browser write.
Assigned faults, full mutation/recovery/archive acceptance and final delivery
remain incomplete. Passing unit tests does not establish those results.

## Start a fresh managed app

Save a connected plan for the app, then apply that exact plan:

```sh
brine plan brine.toml --target TARGET --json
brine apply PREPARATION_PLAN_ID --target TARGET --json
```

For a fresh managed app, this allocates owned empty data and keeps its data fence
held. It starts no native writer or frontend. Use the returned plan ID to deliver
the [four native roles](native-material-commands.md), then deliver the independent
backup password and S3 credentials:

```sh
brine data material set APP --preparation-plan PREPARATION_PLAN_ID \
  --target TARGET --json < private-material.json
brine backup recovery set --deployment-plan PREPARATION_PLAN_ID \
  --target TARGET --json < recovery.json
brine backup credentials plan APP --database NAME --target TARGET --json
brine backup credentials set APP --database NAME --plan-id CREDENTIAL_PLAN_ID \
  --target TARGET --json < private-credentials.json
brine backup policy plan --deployment-plan PREPARATION_PLAN_ID --target TARGET --json
brine backup policy activate BACKUP_PLAN_ID --target TARGET --json
```

Keep `PREPARATION_PLAN_ID` as the ID of the original saved allocation plan whose
apply operation succeeded. Reuse that exact ID for material delivery, recovery
password delivery, backup policy planning and first module publication. The
`--deployment-plan` flag accepts this original unpublished preparation plan in
these steps. Do not reapply it or replace it with a new ordinary deployment plan.

After material delivery, an ordinary `brine plan` reports
`managed_publication_required` and has no executable changes. Complete reviewed
publication using the original preparation plan, then obtain the ordinary
deployment plan shown below.

Material delivery preserves the original fence. It does not require an already
active backup policy. Activate the backup before preparing the original empty
source and reviewing first publication:

```sh
brine backup prepare APP --database NAME --target TARGET --json
brine data module plan APP --point PREPARED_POINT_ID \
  --deployment-plan PREPARATION_PLAN_ID --wasm module/game.wasm --target TARGET --json
brine data module apply APP MODULE_PLAN_ID --target TARGET --json
brine plan brine.toml --target TARGET --json
brine apply DEPLOYMENT_PLAN_ID --target TARGET --json
```

The module review binds owned Wasm bytes, the original empty source and actual
native owner proof. Applying it activates the new data epoch. The following
connected plan starts the declared service and frontend. Later module reviews
also require compatible retained recovery releases.

A completed publication or live restore inherits backup material through its
protected repository result and original source. Brine keeps the actual original
credential and recovery delivery records. It does not invent delivery in the new
epoch. Routine capture selects the new repository automatically.

## Capture an online point

```sh
brine backup capture APP --database NAME --target TARGET --json
```

Use a managed app and its declared database name. The command submits
`native_capture` with only `app`, `database` and `idempotency_key`. The host
derives the binding, epoch, requester, policy and source. The command accepts no
source path, endpoint, credentials, schema or evidence body.

## Prepare exact coverage

```sh
brine backup prepare APP --database NAME --target TARGET --json
```

The command submits `native_prepare` with the same closed fields. Successful
completion identifies a fresh exact prepared point. Its data fence remains held
for the reviewed data operation. This is separate from an ordinary online point
and can require maintenance downtime.

## Abandon unused exact coverage

```sh
brine backup abandon POINT_ID --target TARGET --json
```

The command submits `native_prepare_abandon` with only `point_id` and
`idempotency_key`, under the mutating authorization class. It selects a successful
initialized exact preparation owned by the requester. A fresh or expired unused
point is eligible for reproof. Consumed points, unresolved effects, changed
sources and incompatible current scopes refuse.

The expected receipt has `kind: "native_prepare_abandon"`, `outcome: "abandoned"`,
the original `point_id`, `point_sha256`, `preparation_operation_id`, binding and
epoch, and the source/stop/code/routes proof hashes. It also binds the original
fence owner, compatible release and `stopped_desired_revision`. Completion
atomically releases only the original fence and records the immutable outcome.
Engine, frontend and ingress remain stopped. This is distinct from an online
backup, live restore or ordinary lifecycle start.

Abandonment leaves original point bytes, expiry, journals, data and remote
backups unchanged. It never supplies fresh mutation coverage. After successful
abandonment, ordinary start and a new exact preparation are separate operations.
The [runbook procedure](runbooks/spacetimedb.md#abandon-an-unused-exact-preparation)
describes the required original-operation handling. Actual source53 abandonment
released only its original fence and left all components stopped. The later
ordinary start plan timed out without a saved plan or apply; successful ordinary
resume remains required. The earlier source52 refusal is retained unchanged.

## Test a retained remote point

```sh
brine restore native-test POINT_ID --target TARGET --json
```

Use the exact retained native point ID. The command submits
`native_restore_test` with only `point_id` and `idempotency_key`. The host derives
its app and binding from the protected record. This operation verifies privately
without changing live data; its dispatcher class is read-only. Capture and
preparation use the existing mutating authorization class.

SQLite keeps the existing `restore test` command and receipt. Native testing
does not accept a Litestream TXID or a SQLite receipt.

## Wait or return acceptance

Capture, preparation, restore-test and abandonment wait by default. They report the accepted operation ID on
stderr and poll the existing operation-status API. Completion requires the
closed receipt, the exact task kind and accepted operation ID. Restore-test
and abandonment also require the original requested point ID.

Use `--no-wait` to return only accepted status and operation ID. Acceptance is
not verification. Poll later with:

```sh
brine status --operation OPERATION_ID --target TARGET --json
```

Use `--json` or `--jsonl` for the existing machine envelope. The successful capture, preparation and restore-test
receipt has `kind`, `operation_id`, `binding_id`, `epoch_id`, `point_id`,
`point_sha256`, `outcome: "verified"`, `engine`, `module_sha256`,
`schema_sha256`, `capture_interval_seconds`, `maintenance_millis` and `loss`.
Capture and restore-test report zero maintenance; exact preparation reports
its measured operation interval. Unpublished preparation has empty module and
schema hashes. Loss remains `unknown`. The receipt exposes protected references
and observed facts, not source files or secret material. Human exact-preparation output states that
the data fence remains held.

## Keep the request identity

If `--idempotency-key` is omitted, Brine generates a lowercase 32-character hex
key and reports `idempotency_key=KEY` on stderr before submission. Save it with
the operation ID. To recover a lost response, reuse that key with the same
command inputs. An explicit empty, uppercase, malformed or non-32-character key
refuses before target access.

An interrupted observer does not cancel the detached job. A recovery-required
or mismatched receipt is a refusal, never a successful verification or a second
automatic submission. Inspect the retained operation and its actual effect
before taking another data action.


## Deliver independent recovery material

```sh
brine backup recovery set --deployment-plan DEPLOYMENT_PLAN_ID --target TARGET \
  --idempotency-key KEY --json < recovery.json
```

The private packet is exactly `{"password":"YOUR_INDEPENDENT_RECOVERY_PASSWORD"}`.
The password has 16 to 4096 bytes and contains no NUL. The saved managed
plan selects the recovery reference, binding and epoch. Delivery reserves an
immutable version before writing it and verifies the exact private file before
recording a reference-only receipt. Reusing the same key and password keeps the
same reserved version. A different password with that key refuses.

Recovery material is separate from S3 credentials and the publisher, signing
and auth-policy roles. The archive contains the selected signing private key as
an encrypted member. It does not contain the publisher token or recovery password.

## Activate backup policy

```sh
brine backup policy plan --deployment-plan DEPLOYMENT_PLAN_ID --target TARGET --json
brine backup policy activate BACKUP_PLAN_ID --target TARGET --json
```

The host derives the destination, cadence, retention and exact delivered
credential and recovery versions from protected records. Initial activation
records its repository creation intent, authenticates the encrypted repository
configuration, installs the fixed user service and timer, and reads back their
ownership and loaded timer state before committing the policy. A pending
activation blocks another revision from retrying the same creation.

The timer invokes the same capture operation. Its local command verifies the
exact installed service and actual current systemd invocation before submission.
It does not expose a general local command runner.

## Inspect backup health

```sh
brine backup status APP --database NAME --target TARGET --json
```

Status reads protected metadata. It reports the engine, module and schema,
capture and verification cadence, maintenance budget, selected point and its
capture and verification times. Loss remains `unknown`. A stale verification,
expired credential, pending failed capture or held fence is degraded. Status
never contacts the provider or refreshes a proof timestamp.

`missed_capture_intervals` counts complete capture intervals without a newer
verified capture. `verified_capture_age_seconds` reports the age of the selected
verified source, or `null` when no source has been verified. These measurements
do not prove the amount of data lost.

## Recover an interrupted effect

```sh
brine backup recovery inspect ORIGINAL_OPERATION_ID --target TARGET --json
brine backup recovery review ORIGINAL_OPERATION_ID --target TARGET --json
brine backup recovery resolve REVIEW_ID --target TARGET --json
```

Inspection and review take only the original operation reference. Resolution
submits a new `native_backup_resolve` task using the protected review reference
and an idempotency key. The review expires after ten minutes. The host repeats
readback under fresh guards and refuses a changed observation. The original
operation and its recovery-required outcome stay immutable.

Before an upload, the host retains its exact source, manifest, native state and
confirmed-pair evidence, module and schema bytes, repository scope and physical
workspace identity. Inspection reseals the retained bundle, selects its unique
original capture tag and authenticates the exact remote snapshot. A fixed private
native reader checks complete persisted-state equality and confirmed pairs. No
upload is repeated. A completed original point lets a fresh resolver inspect its
exact stored snapshot after interrupted cleanup, without inventing a replacement
point or reopening an expired preparation permit.

Repository creation uses encrypted configuration readback under the original
issued creation grant. It never calls initialization again. Unit changes are
checked through exact owned files and loaded timer state. A resolver may install
units only when their original installation was affirmatively never issued.
Missing, partial or uncertain issued units refuse automatic resolution.

Prepared recovery also proves the original native/frontend stop, loaded ingress
withdrawal, held original fence and unchanged original source under fresh launch
and lifetime locks. Adopting an already completed prepared point preserves its
original expiry. Online upload exclusion prevents new planned data mutations for
the same binding. It does not create a quiescence fence, stop existing writers,
withdraw ingress or change ordinary same-runtime restart behavior.

Resolution retains its own durable completed record before removing its original
task workspace. Cleanup checks exact held directory identity and actual process
and container consumers through the
[protected observer prerequisite](runbooks/spacetimedb.md#admit-protected-workspace-observation).
Unrelated resources remain untouched. After known absence, cleanup records an
owned retirement intent before deleting that exact directory. A missing directory
after a crash is adopted only with its matching issued intent and descriptor
identity. Missing without that intent, replacement identities and unknown
consumers refuse. Inspection and review propagate cleanup failures even when
their remote read verified successfully. Only after
cleanup succeeds does the task return a success receipt. A point or policy becomes
effective through the actual successful resolver outcome joined to that completed
record. A completed record alone is insufficient.

If inspection refuses, preserve the original private files, operation and any
existing fence. Use operation status and diagnosis to identify the refused stage.
Check supplied credential validity and the original owned source, services and
workspace. An expired credential needs externally supplied scoped renewal through
the existing credential workflow; Brine does not mint credentials. Active or
unidentifiable consumers, changed ownership, missing proof and partial issued
units need a manual, target-specific review. Do not edit control records, delete
remote objects or retry an uncertain mutation to produce a success response.
