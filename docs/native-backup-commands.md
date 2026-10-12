# Native backup command contract

The commands use the existing detached operation engine and protected native
backup records. Capture reads the running source, encrypts its selected native
history and signing recovery material, uploads it, then independently restores
and verifies it. Preparation withdraws owned ingress and proves a clean stop
before making exact coverage. Both retain unknown effects for inspection.

Physical S3, Pi, browser and interruption acceptance remains pending. Passing
unit tests does not establish those results.

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

All three commands wait by default. They report the accepted operation ID on
stderr and poll the existing operation-status API. Completion requires the
closed `nativecapture.TaskReceipt`, the exact task kind and accepted operation
ID. Native restore-test also requires the original requested point ID.

Use `--no-wait` to return only accepted status and operation ID. Acceptance is
not verification. Poll later with:

```sh
brine status --operation OPERATION_ID --target TARGET --json
```

Use `--json` or `--jsonl` for the existing machine envelope. The successful native
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

## Unknown effects

An upload, repository initialization or unit change that loses its result remains
recovery-required. Its retained intent and partial private files stay available.
A new activation cannot bypass pending creation by using another plan. Automated
resolution of these backup effects is still pending; inspect the existing
operation and preserve its files and fence. Do not edit control records or retry
an uncertain mutation to make the command report success.
