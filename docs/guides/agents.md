# Automating Brine

Use the enrolled personal identity for both review and apply. Keep the same target and config directory on every command. Brine's restricted channel accepts typed operations, not a shell. D8 refuses raw Podman, systemd and Caddy access, unrelated software and files, network administration and cloud machine creation or deletion. These limits keep the operator's policy and recorded operations in control; a request cannot grant itself approval.

Plan a restart and inspect the JSON envelope:

```sh
brine restart hello --target staging --config-dir /path/to/config --json
```

Check the process exit category and `ok` together. Exit 0 is success; 1 is operational, 2 validation, 3 dependency, 4 policy, 5 state conflict, 6 recovery and 130 interruption. On error, inspect `error.code`; do not infer success from output alone. Review the plan before applying. Only `data.kind` of `create` or `update` is applyable; `no-op`, `conflict`, a missing kind, or an unsuccessful envelope must not be applied. Select and save a unique idempotency key once before the first apply. If acceptance is uncertain, recover with the same plan and key, never a fresh key.

```sh
brine apply PLAN_ID --idempotency-key SAVED_KEY --target staging --config-dir /path/to/config --json
brine status --operation OPERATION_ID --after-cursor 0 --target staging --config-dir /path/to/config --json
```

Save `data.operation_id` from acceptance. Poll using each response's `data.next_cursor` as the next `--after-cursor`. `succeeded` is success; `failed`, `rolled_back` and `recovery_required` are failures. `launch_unknown` is unsettled: stop polling, retain the operation ID and cursor, and inspect without changing the host. Never relaunch it automatically.

```sh
brine diagnose hello --target staging --config-dir /path/to/config --json
brine reconcile --dry-run --target staging --config-dir /path/to/config --json
brine resolve OPERATION_ID --idempotency-key RECOVERY_KEY --target staging --config-dir /path/to/config --json
```

Run `diagnose` first when something is wrong. Review the dry-run reconciliation result before deciding on recovery; `resolve` is for a terminal recovery-required operation and needs its own saved key. Do not assume an app rollback restores data. The example at `examples/automation/restart.sh` takes the app, target, config directory and saved key as arguments. It requires POSIX sh and jq.

Use JSON for automation, not the TUI. JSONL is bounded response framing, not a live operation stream; poll status for progress. See [the machine contract](../CONTRACTS.md) for envelope fields and exit categories.
