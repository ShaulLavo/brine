#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Versioned cache paths cannot silently reuse an older installed analyzer.
export BRINE_TOOLS="${BRINE_TOOLS:-${XDG_CACHE_HOME:-$HOME/.cache}/brine-tools}"
export BRINE_CHECK_OUTPUT="${BRINE_CHECK_OUTPUT:-$PWD/.tmp/checks}"
mkdir -p "$BRINE_CHECK_OUTPUT"

pinned_tool() {
  local name=$1 version=$2 module=$3
  local bin="$BRINE_TOOLS/$name-$version/$name"
  if [[ ! -x $bin ]]; then
    mkdir -p "$(dirname "$bin")" || return
    GOBIN="$(dirname "$bin")" go install "$module@$version" || return
  fi
  printf '%s\n' "$bin"
}

golangci() {
  local bin="${GOLANGCI_LINT_BIN:-}" version
  if [[ -z $bin ]]; then
    if command -v golangci-lint > /dev/null && version=$(golangci-lint version 2>/dev/null) && [[ $version == *'version 2.14.0 '* ]]; then
      bin=golangci-lint
    else
      bin=$(pinned_tool golangci-lint v2.14.0 github.com/golangci/golangci-lint/v2/cmd/golangci-lint)
    fi
  fi
  if [[ $("$bin" version) != *'version 2.14.0 '* ]]; then
    echo 'golangci-lint v2.14.0 is required. Run mise install.' >&2
    return 1
  fi
  "$bin" "$@"
}

rules() {
  go run ./tools/checkgate rules --baseline tools/baselines/rules.json
}

duplicates() {
  # A nonzero analyzer exit still fails; only findings are passed to the baseline gate.
  golangci run --config .golangci-dupl.yml --issues-exit-code=0 \
    --output.json.path="$BRINE_CHECK_OUTPUT/duplicates.json" \
    --output.text.path="$BRINE_CHECK_OUTPUT/duplicates.txt" ./...
  go run ./tools/checkgate duplicates --input "$BRINE_CHECK_OUTPUT/duplicates.json" \
    --baseline tools/baselines/duplicates.json
}

deadcode() {
  local bin
  bin=$(pinned_tool deadcode v0.51.0 golang.org/x/tools/cmd/deadcode)
  # Load every package so unimported packages are checked too. Only main packages
  # supply reachability roots; tests never make unused production code appear live.
  GOOS=linux GOARCH=amd64 "$bin" -json ./... > "$BRINE_CHECK_OUTPUT/deadcode.json"
  go run ./tools/checkgate deadcode --input "$BRINE_CHECK_OUTPUT/deadcode.json" \
    --allow tools/baselines/deadcode-allowlist.json --baseline tools/baselines/deadcode-linux.json
}

lint() {
  local base="${BRINE_BASE:-}"
  if [[ -z $base ]]; then
    base=$(git merge-base HEAD origin/main) || return
  fi
  golangci run --new-from-merge-base= --new-from-rev="$base" ./...
  local config="$PWD/.golangci.yml"
  (cd tools/webtransport-experiment && golangci run --config "$config" --new-from-merge-base= --new-from-rev= ./...)
}

fast() {
  local changed dir
  local packages=()
  changed=$(git diff --name-only HEAD -- '*.go') || return
  # Staged-file checks alone cannot see a new import boundary or a cross-package clone.
  rules
  duplicates
  while IFS= read -r dir; do
    if [[ $dir == tools/webtransport-experiment ]]; then
      local config="$PWD/.golangci.yml"
      (cd "$dir" && golangci run --config "$config" --new-from-merge-base= --new-from-rev= ./...)
      continue
    fi
    if [[ -d $dir ]] && compgen -G "$dir/*.go" > /dev/null; then
      packages+=("./$dir")
    fi
  done < <(printf '%s\n' "$changed" | sed 's|[^/]*$||; s|/$||; s|^$|.|' | sort -u)
  if [[ ${#packages[@]} -gt 0 ]]; then
    golangci run --new-from-merge-base= --new-from-rev=HEAD "${packages[@]}"
  fi
}

format_check() {
  local unformatted
  unformatted=$(gofmt -l ./cmd ./internal ./integration ./tools)
  if [[ -n $unformatted ]]; then
    printf '%s\n' "$unformatted" >&2
    return 1
  fi
  ./scripts/test-docs-commands.sh
  git diff --check
  git diff --check "$(git hash-object -t tree /dev/null)" HEAD
}

tidy() {
  go mod tidy
  git diff --exit-code -- go.mod go.sum
}

normal_tests() {
  format_check
  tidy
  go vet ./...
  GOOS=darwin GOARCH=arm64 go vet ./...
  # Shared disk and process fixtures make package-level parallelism scheduler-sensitive.
  BRINE_REQUIRE_NODE=1 go test -p 1 -timeout 30m ./...
  go build ./cmd/brine
  (
    cd tools/webtransport-experiment
    go mod tidy
    git diff --exit-code -- go.mod go.sum
    go vet ./...
    GOOS=darwin GOARCH=arm64 go vet ./...
    go test -v -count=1 -timeout 1m ./...
  )
}

race_tests() {
  race_shard all
}

# Pure-Go SQLite migration fixtures can exceed 30 minutes under -race.
# Keep those two shards bounded inside the 45-minute CI job limit.
race_shard() {
  local packages timeout=30m
  local semantic=()
  case $1 in
    store) packages=(./internal/store/); timeout=40m ;;
    host) packages=(./internal/host/); timeout=40m ;;
    rest) mapfile -t packages < <(go list ./... | command grep -v -x -e github.com/ShaulLavo/brine/internal/store -e github.com/ShaulLavo/brine/internal/host) ;;
    all) packages=(./...); timeout=40m ;;
    *) echo "unknown race shard: $1" >&2; return 2 ;;
  esac
  # Worker semantics run once under the explicitly installed Node in normal tests.
  if [[ $1 == rest || $1 == all ]]; then
    semantic=(-skip '^TestWorkerSemanticContract$')
  fi
  go test -race -p 1 -timeout "$timeout" "${semantic[@]}" "${packages[@]}"
  if [[ $1 == rest || $1 == all ]]; then
    (
      cd tools/webtransport-experiment
      go test -race -count=1 -timeout 1m ./...
    )
  fi
}

# The host race suite is one serial package that runs for about 40 minutes. CI builds
# its race test binary once and runs disjoint selections of it on separate runners.
host_race_shards=4

host_race_build() {
  local dir=${1:?usage: check.sh test-race-host-build DIR}
  mkdir -p "$dir"
  go test -race -p 1 -c -o "$dir/host.test" ./internal/host/
  git rev-parse HEAD > "$dir/source-head"
  (cd "$dir" && sha256sum host.test > host.test.sha256)
  host_race_cover "$dir"
}

# Sorted round-robin assignment also covers new tests without a maintained name list.
host_race_partition() {
  local binary=$1
  (cd internal/host && "$binary" -test.list '.*') |
    LC_ALL=C command grep -E '^(Test|Example|Fuzz)' |
    LC_ALL=C sort |
    LC_ALL=C awk -v shards="$host_race_shards" '{ printf "%d\t%s\n", (NR - 1) % shards + 1, $0 }'
}

host_race_cover() {
  local dir=$1 binary listed assigned
  binary=$(realpath "$dir/host.test")
  listed=$( (cd internal/host && "$binary" -test.list '.*') | LC_ALL=C command grep -E '^(Test|Example|Fuzz)' | LC_ALL=C sort)
  assigned=$(host_race_partition "$binary" | cut -f2 | LC_ALL=C sort)
  if [[ -z $listed || $listed != "$assigned" ]] || [[ -n $(uniq -d <<< "$assigned") ]]; then
    echo 'Host race shards must select every listed test exactly once.' >&2
    return 1
  fi
  host_race_partition "$binary" | LC_ALL=C awk -F '\t' '{ n[$1]++ } END { for (s in n) printf "host race shard %s: %d tests\n", s, n[s] }' | LC_ALL=C sort
}

host_race_run() {
  local index=${1:?usage: check.sh test-race-host-shard INDEX DIR [TEST-FLAG...]} dir=${2:?usage: check.sh test-race-host-shard INDEX DIR [TEST-FLAG...]}
  local binary selected regex listed
  shift 2
  if [[ ! $index =~ ^[1-9][0-9]*$ ]] || (( index > host_race_shards )); then
    echo "host race shard must be 1 through $host_race_shards: $index" >&2
    return 2
  fi
  # The binary must come from this checkout so its tests read matching testdata.
  [[ $(cat "$dir/source-head") == "$(git rev-parse HEAD)" ]] || { echo 'Host race binary was built from another commit.' >&2; return 1; }
  (cd "$dir" && sha256sum --check --quiet host.test.sha256)
  # Helper tests re-execute os.Args[0] from other directories, so pass an absolute path.
  binary=$(realpath "$dir/host.test")
  selected=$(host_race_partition "$binary" | LC_ALL=C awk -F '\t' -v shard="$index" '$1 == shard { print $2 }' | LC_ALL=C sort)
  [[ -n $selected ]] || { echo "Host race shard $index selected no tests." >&2; return 1; }
  regex="^($(paste -sd '|' <<< "$selected"))\$"
  listed=$( (cd internal/host && "$binary" -test.list "$regex") | LC_ALL=C sort)
  [[ $listed == "$selected" ]] || { echo "Host race shard $index regex does not select exactly its tests." >&2; return 1; }
  printf 'host race shard %s of %s: %d top-level tests\n' "$index" "$host_race_shards" "$(wc -l <<< "$selected")"
  (cd internal/host && "$binary" -test.paniconexit0 -test.timeout 40m -test.count=1 -test.run "$regex" "$@")
}

tests() {
  normal_tests
  race_tests
}

vulnerability() {
  local bin
  bin=$(pinned_tool govulncheck v1.7.0 golang.org/x/vuln/cmd/govulncheck)
  "$bin" ./...
  (cd tools/webtransport-experiment && "$bin" -test ./...)
}

case "${1:-all}" in
  all) bun test tools/ci/udp-fixture tools/ci/release; tests; lint; rules; deadcode; duplicates; vulnerability ;;
  test) tests ;;
  test-helpers) bun test tools/ci/udp-fixture tools/ci/release ;;
  test-normal) normal_tests ;;
  test-race) race_tests ;;
  test-race-store) race_shard store ;;
  test-race-host) race_shard host ;;
  test-race-host-build) host_race_build "${2:-}" ;;
  test-race-host-shard) host_race_run "${@:2}" ;;
  test-race-host-shards) printf '[%s]\n' "$(seq -s , "$host_race_shards")" ;;
  test-race-rest) race_shard rest ;;
  lint) lint ;;
  structure) rules; deadcode; duplicates ;;
  vulnerability) vulnerability ;;
  fast) fast ;;
  format) format_check ;;
  *) echo 'Usage: scripts/check.sh [all|test|test-helpers|test-normal|test-race|test-race-store|test-race-host|test-race-host-build DIR|test-race-host-shard INDEX DIR [TEST-FLAG...]|test-race-host-shards|test-race-rest|lint|structure|vulnerability|fast|format]' >&2; exit 2 ;;
esac
