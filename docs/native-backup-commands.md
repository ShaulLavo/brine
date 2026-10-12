# Native backup command contract

This command source is a consumer checkpoint for the combined P05 delivery.
Production capture, preparation and remote restore acceptance still require
P05's protected host controller and real storage evidence. Fake-backed command
tests establish request, authorization, polling and presentation behavior only.

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
`point_sha256` and `outcome: "verified"`. It exposes protected references,
not source files or secret material. Human exact-preparation output states that
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
