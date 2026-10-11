#!/usr/bin/env bash
set -euo pipefail

# Run from the repository root.
UPDATE_ENGINE_GOLDEN=0 go test -p 1 -run 'Engine|StartupIdentity|PersistedSQLite|SQLiteReceipt' \
  ./internal/data ./internal/spec ./internal/plan ./internal/restore \
  ./internal/replication ./internal/store
