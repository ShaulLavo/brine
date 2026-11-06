#!/bin/sh
# Usage: restart.sh APP TARGET CONFIG_DIR IDEMPOTENCY_KEY
set -eu

if [ "$#" -ne 4 ]; then
  echo 'Usage: restart.sh APP TARGET CONFIG_DIR IDEMPOTENCY_KEY' >&2
  exit 2
fi
app=$1
target=$2
config_dir=$3
key=$4
BRINE=${BRINE:-brine}
interval=${POLL_INTERVAL:-1}
if [ -z "$key" ]; then
  echo 'Select and save an idempotency key before planning.' >&2
  exit 2
fi

diagnose() {
  "$BRINE" diagnose "$app" --target "$target" --config-dir "$config_dir" --json >&2 || :
}
plan=$("$BRINE" restart "$app" --target "$target" --config-dir "$config_dir" --json) || {
  printf '%s\n' "$plan" >&2
  diagnose
  exit 1
}
plan_id=$(printf '%s\n' "$plan" | jq -er 'select(.ok == true and (.data.kind == "create" or .data.kind == "update")) | .data.plan_id | select(type == "string" and length > 0)') || {
  echo 'Plan is not applyable; inspect the response.' >&2
  printf '%s\n' "$plan" >&2
  exit 1
}
# Save this key before apply. If acceptance is uncertain, recover with the same key.
printf 'idempotency_key=%s\n' "$key" >&2
accepted=$("$BRINE" apply "$plan_id" --idempotency-key "$key" --target "$target" --config-dir "$config_dir" --json) || {
  printf '%s\n' "$accepted" >&2
  echo 'Acceptance may be unknown; retain the plan and key for recovery.' >&2
  diagnose
  exit 1
}
operation=$(printf '%s\n' "$accepted" | jq -er 'select(.ok == true and .data.status == "accepted") | .data.operation_id | select(type == "string" and length > 0)') || exit 1
cursor=0
while :; do
  page=$("$BRINE" status --operation "$operation" --after-cursor "$cursor" --target "$target" --config-dir "$config_dir" --json) || {
    printf '%s\n' "$page" >&2
    diagnose
    exit 1
  }
  state=$(printf '%s\n' "$page" | jq -er 'select(.ok == true) | .data.operation.state | select(type == "string")') || exit 1
  cursor=$(printf '%s\n' "$page" | jq -er 'select(.ok == true) | .data.next_cursor | select(type == "number" and . >= 0 and floor == .)') || exit 1
  case "$state" in
    succeeded) printf '%s\n' "$page"; exit 0 ;;
    failed|rolled_back|recovery_required)
      printf '%s\n' "$page" >&2
      diagnose
      exit 1 ;;
    launch_unknown)
      printf 'Operation %s has an unknown launch outcome at cursor %s. Inspect without changing it:\n' "$operation" "$cursor" >&2
      printf 'brine reconcile --dry-run --target ' >&2
      printf "'%s'" "$(printf '%s' "$target" | sed "s/'/'\\\\''/g")" >&2
      printf ' --config-dir ' >&2
      printf "'%s'\n" "$(printf '%s' "$config_dir" | sed "s/'/'\\\\''/g")" >&2
      exit 1 ;;
    queued|preflight|preparing|quiescing|starting|checking|committing|rolling_back) ;;
    *) echo "Unexpected operation state: $state" >&2; exit 1 ;;
  esac
  sleep "$interval"
done
