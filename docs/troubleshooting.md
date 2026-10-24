# Troubleshooting

Find the symptom, read what it means, and follow the fix. Every code below is in the table in [CONTRACTS](CONTRACTS.md#error-codes-and-exit-categories). With `--json`, the code is in `.error.code` and the exit status is the number in the table.

## A mutation timed out or the connection dropped

**Symptom:** `transport_failure` (exit 1), a lost response from `apply`, or Ctrl-C during `apply`.

Ctrl-C and a dropped SSH session stop only your observer. The operation keeps running on the server. The error message tells you to reconcile before you retry, and that is the rule.

1. Rerun the same `apply` with the same `--idempotency-key`. The key finds the existing operation. A new key starts a second one.
2. If you lost the key, do not guess. Inspect first:

   ```sh
   brine reconcile --target NAME --dry-run
   brine diagnose APP --target NAME
   ```

3. Poll the operation with `brine status --operation OPERATION_ID --target NAME` until its state is terminal.

## An operation ended in `recovery_required`

**Symptom:** exit 6, or an operation whose state is `recovery_required`.

Brine stopped because it could not prove what state the server is in. It never settles that by repeating the mutation.

1. Run `brine diagnose APP --target NAME` and `brine reconcile --target NAME --dry-run`. Both are read-only.
2. Run `brine reconcile --target NAME` to settle interrupted operations, or `brine resolve OPERATION_ID --target NAME` for a terminal operation that needs resolution.
3. Plan again before you change anything else.

`recovery_required` is terminal for that operation. A successful resolve creates a successor and keeps the original record.

## `plan` succeeded but the plan is a conflict

**Symptom:** exit 0, `kind` is `conflict`, and `conflicts` lists codes such as `runtime_unavailable` or `unknown_facts`.

A conflict plan is a normal result that carries no executable changes. Never apply it. `runtime_unavailable` means the host lacks a required runtime (the `field` says which). `unknown_facts` means Brine could not measure something and will not guess. Fix the host or the app definition, then plan again. [Getting started](getting-started.md#preview-a-plan-for-an-unenrolled-host) shows a real conflict plan.

## The operator policy refused the app

**Symptom:** `policy_refused` or `policy_registry_denied`, exit 4.

The message is fixed on purpose and does not say which rule failed. Check the app against the policy by hand:

- The image registry and repository must match an `allowed_registries` prefix.
- Every domain must match `allowed_domains`.
- Every secret reference must appear in `allowed_secrets` for the app.
- Resource requests must fit the `resources` ceilings.
- Database roots must sit under `persistent_roots`.

A fresh enrollment denies all of these. Use `brine permissions plan` for registries and domains. Edit the policy file as root for the rest. See the [configuration reference](reference/configuration.md#operator-policy).

## A plan is stale or another operation holds the lock

**Symptom:** `conflict`, exit 5, `retryable: true`.

The plan no longer matches the server, or another operation holds the host lock. Wait for the other operation, then run `brine plan` again and apply the new plan ID. Do not retry the old plan ID.

## `brine rollback` refuses

- `app_not_found` (exit 2): the app has no committed release.
- `release_not_found` (exit 2): the `--release` ID does not belong to that app.
- `rollback_no_op` (exit 5): the target release is already current.

## `brine remove` refuses an app with data

**Symptom:** `persistent_archive_required`, exit 4.

Removal refuses and deletes nothing. Retiring a persistent SQLite app is not implemented yet (task P04-08 is open). For a managed SpacetimeDB app, follow [Archive and retain the app](runbooks/spacetimedb.md#archive-and-retain-the-app).

## A command asks for input or a terminal

- `input_required` (exit 2): the command needs a prompt, but `--no-input`, `--json` or `--jsonl` forbids it. Supply the arguments, or run it in a terminal.
- `brine enroll` needs a terminal and a typed target name, even with `--yes`.
- `tui_terminal_required` and `tui_interactive` (exit 2): `brine tui` needs a real terminal and none of the machine flags.

## A required tool is missing

**Symptom:** `dependency_missing`, exit 3.

Run `brine doctor`. It checks the client only: `ssh` is required, `git` is optional. It does not check the server.

## `plan` says to choose a target

**Symptom:** `offline_required`, exit 2.

Connected planning needs `--target NAME`. An offline preview needs `--offline` with `--snapshot` and `--policy`.

## `brine logs` returns an error

- `logs_container_unavailable` and `logs_journal_unavailable` (exit 3): the host cannot read that log source. App logs need Podman with the `k8s-file` log driver. Unit logs need readable journals.
- `logs_ownership_refused` (exit 4): logs are readable only for apps that Brine owns.
- `logs_truncated` and `logs_limit_exceeded` (exit 1): request a smaller `--tail`. The limit is 1 to 1000 entries.

## `dispatch_operation_refused`

**Symptom:** exit 4, "The dispatcher operation is not allowed."

The restricted dispatcher on the server refused the request. It accepts only the operations in its allowlist ([D8](DECISIONS.md#d8-agents-manage-the-whole-lifecycle-through-brine-operations-only)), and never lets the client choose a command. Run `brine diagnose` and `brine status --target NAME` to see whether the target answers at all, and check that you pass the same `--target` and `--config-dir` as at enrollment.

## Still stuck

Collect `brine version`, the command, its exit code and the JSON error, and open an issue. Leave out secret values and real credentials. For a security problem, follow [SECURITY.md](../SECURITY.md) instead.
