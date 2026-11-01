#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/brine-ci-tests-XXXXXX")
trap 'rm -rf "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

check_scope() {
  local old=$1 new=$2 want=$3 base got
  local fixture="$scratch/repo-$((++count))"
  mkdir -p "$fixture/$(dirname "$old")"
  git -C "$fixture" init -q
  git -C "$fixture" config core.hooksPath /dev/null
  git -C "$fixture" config user.name 'CI fixture'
  git -C "$fixture" config user.email ci@example.invalid
  printf 'before\n' > "$fixture/$old"
  git -C "$fixture" add .
  git -C "$fixture" -c commit.gpgsign=false commit -qm base
  base=$(git -C "$fixture" rev-parse HEAD)
  got=$(cd "$fixture" && "$root/scripts/ci-scope.sh" "$base" "$base")
  [[ $got == full ]]
  rm -- "$fixture/$old"
  if [[ -n $new ]]; then
    mkdir -p "$fixture/$(dirname "$new")"
    printf 'before\n' > "$fixture/$new"
    if [[ $old == "$new" ]]; then
      printf 'after\n' >> "$fixture/$new"
    fi
  fi
  mkdir -p "$fixture/docs"
  printf 'mixed documentation change\n' > "$fixture/docs/also.md"
  git -C "$fixture" add -A
  git -C "$fixture" -c commit.gpgsign=false commit -qm change
  got=$(cd "$fixture" && "$root/scripts/ci-scope.sh" "$base" HEAD)
  if [[ $got != "$want" ]]; then
    printf 'scope %q -> %q = %q, expected %q\n' "$old" "$new" "$got" "$want" >&2
    exit 1
  fi
}

count=0
for path in README.md docs/CONTRACTS.md 'docs/guides/new guide.md' .internal/plans/release.md docs/assets/hero.webp; do
  check_scope "$path" "$path" docs
done
for path in internal/example.go internal/testdata/input.md fixtures/app/README.md docs/testdata/input.md docs/Fixtures/input.md docs/fixture/input.md docs/example.go docs/example.toml docs/assets/new.wasm .github/workflows/ci.yml scripts/check.sh go.sum mise.toml unknown.txt; do
  check_scope "$path" "$path" full
done
check_scope docs/old.md docs/new.md docs
check_scope docs/removed.md '' docs
check_scope internal/example.go docs/example.md full
check_scope docs/example.md internal/example.go full
check_scope internal/deleted.go '' full
check_scope $'internal/line\nbreak.go' $'docs/line\nbreak.md' full
for ref in missing HEAD; do
  if (cd "$root" && ./scripts/ci-scope.sh "$ref" missing) > "$scratch/invalid" 2>&1; then
    echo 'Invalid refs must not classify successfully.' >&2
    exit 1
  fi
done
if "$root/scripts/ci-scope.sh" > "$scratch/invalid" 2>&1; then
  echo 'Missing refs must not classify successfully.' >&2
  exit 1
fi

mkdir "$scratch/bin"
cat > "$scratch/bin/go" <<'MOCK'
#!/usr/bin/env bash
set -eu
where=root
[[ $PWD != */tools/webtransport-experiment ]] || where=experiment
printf '%s|%s|%s %s\n' "$where" "${GOOS:-}" "${GOARCH:-}" "$*" >> "$COMMAND_LOG"
if [[ $* == 'list ./...' ]]; then
  printf 'github.com/ShaulLavo/brine/internal/%s\n' store host cli
fi
MOCK
printf '#!/bin/sh\nexit 0\n' > "$scratch/bin/git"
printf '#!/bin/sh\nexit 0\n' > "$scratch/bin/gofmt"
chmod 700 "$scratch/bin/"*

check_commands() {
  local mode=$1
  cat > "$scratch/expected"
  : > "$scratch/commands"
  PATH="$scratch/bin:$PATH" COMMAND_LOG="$scratch/commands" GOOS='' GOARCH='' BRINE_CHECK_OUTPUT="$scratch/output" \
    "$root/scripts/check.sh" "$mode"
  diff -u "$scratch/expected" "$scratch/commands"
}

check_commands test-normal <<'EXPECTED'
root|| mod tidy
root|| vet ./...
root|darwin|arm64 vet ./...
root|| test -p 1 -timeout 30m ./...
root|| build ./cmd/brine
experiment|| mod tidy
experiment|| vet ./...
experiment|darwin|arm64 vet ./...
experiment|| test -v -count=1 -timeout 1m ./...
EXPECTED
check_commands test-race-store <<'EXPECTED'
root|| test -race -p 1 -timeout 40m ./internal/store/
EXPECTED
check_commands test-race-host <<'EXPECTED'
root|| test -race -p 1 -timeout 40m ./internal/host/
EXPECTED
check_commands test-race-rest <<'EXPECTED'
root|| list ./...
root|| test -race -p 1 -timeout 30m github.com/ShaulLavo/brine/internal/cli
experiment|| test -race -count=1 -timeout 1m ./...
EXPECTED
printf 'CI classifier and exact Go command tests passed.\n'
