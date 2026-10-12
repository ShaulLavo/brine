#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

: "${BRINE_RESTIC_GATE_SHA256:?Set the reviewed SHA-256 of root-owned /usr/bin/restic 0.19.1}"
: "${TMPDIR:?Set a private test scratch directory on the data drive}"
export BRINE_RESTIC_GATE=1
go test -count=1 -v -run '^TestResticLocalEncryptedRoundTrip$' ./internal/restic
