#!/usr/bin/env bash
set -euo pipefail
export PATH=$HOME/.local/share/mise/shims:$PATH
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GONOPROXY=none
: "${TMPDIR:?set a task-owned TMPDIR on /work}"
if (( $# != 1 )); then
  echo 'Usage: integration/udp-delivery/run-local.sh NEW_EVIDENCE_DIRECTORY' >&2
  exit 2
fi
repo_dir=$(cd "$(dirname "$0")/../.." && pwd)
evidence=$1
[[ $evidence == /* && ! -e $evidence ]]
scratch=''
cleanup() {
  if [[ -n $scratch ]]; then rm -rf -- "$scratch"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -m 700 "$evidence"
{
  date -u +%FT%TZ
  uname -sm
  go version
  ps -u "$(id -u)" -o pid=,comm=
  ss -H -lntu
} > "$evidence/baseline.txt"
scratch=$(mktemp -d "$TMPDIR/udp-delivery.XXXXXX")
export GOTMPDIR=$scratch
cd "$repo_dir"
go test -mod=readonly -count=1 -timeout 30s -v ./fixtures/udp-echo/... ./integration/udp-delivery > "$evidence/tests.txt" 2>&1
go test -mod=readonly -race -count=1 -timeout 1m ./fixtures/udp-echo/... ./integration/udp-delivery > "$evidence/race.txt" 2>&1
go vet -mod=readonly ./fixtures/udp-echo/... ./integration/udp-delivery > "$evidence/vet.txt" 2>&1
fixtures/udp-echo/build.sh "$scratch/release-a" release-a
fixtures/udp-echo/build.sh "$scratch/release-a-repeat" release-a
fixtures/udp-echo/build.sh "$scratch/release-b" release-b
diff -u "$scratch/release-a/SHA256SUMS" "$scratch/release-a-repeat/SHA256SUMS"
if cmp -s "$scratch/release-a/amd64/udp-echo-server" "$scratch/release-b/amd64/udp-echo-server"; then
  echo 'Distinct releases produced identical server binaries.' >&2
  exit 1
fi
cp "$scratch/release-a/build.txt" "$evidence/release-a-build.txt"
cp "$scratch/release-b/build.txt" "$evidence/release-b-build.txt"
cp "$scratch/release-a/SHA256SUMS" "$evidence/release-a-SHA256SUMS"
cp "$scratch/release-b/SHA256SUMS" "$evidence/release-b-SHA256SUMS"
python3 integration/udp-delivery/smoke.py "$scratch/release-a" "$scratch/release-b" "$evidence"
rm -rf -- "$scratch"
[[ ! -e $scratch ]]
printf 'Task build directories removed. Smoke driver verified all fixture subprocesses stopped and both socket types released.\n' > "$evidence/cleanup.txt"
scratch=''
