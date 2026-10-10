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
  # Host operations exist only in the Linux executable. Mac client vet runs separately.
  GOOS=linux GOARCH=amd64 "$bin" -json ./cmd/brine > "$BRINE_CHECK_OUTPUT/deadcode.json"
  go run ./tools/checkgate deadcode --input "$BRINE_CHECK_OUTPUT/deadcode.json" \
    --allow tools/baselines/deadcode-allowlist.json --baseline tools/baselines/deadcode-linux.json
}

lint() {
  local base="${BRINE_BASE:-}"
  if [[ -z $base ]]; then
    base=$(git merge-base HEAD origin/main) || return
  fi
  golangci run --new-from-merge-base= --new-from-rev="$base" ./...
}

fast() {
  local changed dir
  local packages=()
  changed=$(git diff --name-only HEAD -- '*.go') || return
  # Staged-file checks alone cannot see a new import boundary or a cross-package clone.
  rules
  duplicates
  while IFS= read -r dir; do
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
  git diff --check
  git diff --check "$(git hash-object -t tree /dev/null)" HEAD
}

tidy() {
  go mod tidy
  git diff --exit-code -- go.mod go.sum
}

tests() {
  format_check
  tidy
  go vet ./...
  GOOS=darwin GOARCH=arm64 go vet ./...
  # Shared disk and process fixtures make package-level parallelism scheduler-sensitive.
  go test -p 1 -timeout 30m ./...
  go test -race -p 1 -timeout 30m ./...
  go build ./cmd/brine
}

vulnerability() {
  local bin
  bin=$(pinned_tool govulncheck v1.7.0 golang.org/x/vuln/cmd/govulncheck)
  "$bin" ./...
}

case "${1:-all}" in
  all) tests; lint; rules; deadcode; duplicates; vulnerability ;;
  test) tests ;;
  lint) lint ;;
  structure) rules; deadcode; duplicates ;;
  vulnerability) vulnerability ;;
  fast) fast ;;
  format) format_check ;;
  *) echo 'Usage: scripts/check.sh [all|test|lint|structure|vulnerability|fast|format]' >&2; exit 2 ;;
esac
