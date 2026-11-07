#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../../.."
names=(TestInitCommandModes TestInitGeneratedSpecPlans TestInitPreservesFiles TestInitFailureCleanup TestInitRunnerApproval)
listed=$(go test ./internal/cli -list '^TestInit')
for name in "${names[@]}"; do
  grep -Fx "$name" <<< "$listed" > /dev/null
done
go test "$@" ./internal/cli -run '^TestInit' -count=1 -timeout=3m
