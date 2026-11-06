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
for path in internal/example.go internal/testdata/input.md fixtures/app/README.md docs/testdata/input.md docs/Fixtures/input.md docs/fixture/input.md docs/example.go docs/example.toml docs/assets/new.wasm .github/workflows/ci.yml scripts/check.sh scripts/publish-udp-fixture.ts scripts/build-release.ts scripts/release.ts tools/ci/udp-fixture/*.ts tools/ci/release/*.ts .github/workflows/release.yml fixtures/udp-echo/pins.json .github/workflows/udp-fixture.yml internal/cdn/cloudflare/worker.test.mjs internal/cdn/testdata/eligibility.json go.sum mise.toml unknown.txt; do
  check_scope "$path" "$path" full
done
check_scope docs/old.md docs/new.md full
check_scope docs/removed.md '' full
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
if [[ $* == 'test -p 1 -timeout 30m ./...' ]]; then
  [[ ${BRINE_REQUIRE_NODE:-} == 1 ]] || { echo 'Normal tests must require Node.' >&2; exit 89; }
fi
if [[ $* == 'list ./...' ]]; then
  printf 'github.com/ShaulLavo/brine/internal/%s\n' store host cli
fi
if [[ $* == 'test -race -p 1 -c -o '* ]]; then
  cp "$FAKE_HOST_TEST" "$7"
fi
MOCK
cat > "$scratch/bin/bun" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf 'bun|%s\n' "$*" >> "$COMMAND_LOG"
exit "${HELPER_TEST_STATUS:-0}"
MOCK
printf '#!/bin/sh\nexit 0\n' > "$scratch/bin/git"
printf '#!/bin/sh\nexit 0\n' > "$scratch/bin/gofmt"
chmod 700 "$scratch/bin/"*

check_commands() {
  cat > "$scratch/expected"
  : > "$scratch/commands"
  PATH="$scratch/bin:$PATH" COMMAND_LOG="$scratch/commands" GOOS='' GOARCH='' BRINE_CHECK_OUTPUT="$scratch/output" \
    FAKE_HOST_TEST="$scratch/fake-host.test" "$root/scripts/check.sh" "$@" > "$scratch/stdout" || return
  diff -u "$scratch/expected" "$scratch/commands"
}

check_commands test-helpers <<'EXPECTED'
bun|test tools/ci/udp-fixture tools/ci/release
EXPECTED
if HELPER_TEST_STATUS=17 check_commands test-helpers <<'EXPECTED'
bun|test tools/ci/udp-fixture tools/ci/release
EXPECTED
then
  echo 'A failing helper assertion must fail its CI command.' >&2
  exit 1
else
  [[ $? == 17 ]]
fi

mkdir "$scratch/without-bun"
for name in bash dirname mkdir; do
  ln -s "$(command -v "$name")" "$scratch/without-bun/$name"
done
if PATH="$scratch/without-bun" BRINE_CHECK_OUTPUT="$scratch/output" "$root/scripts/check.sh" test-helpers > "$scratch/missing-bun" 2>&1; then
  echo 'Missing Bun must fail the helper gate.' >&2
  exit 1
else
  [[ $? == 127 ]]
fi

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
root|| test -race -p 1 -timeout 30m -skip ^TestWorkerSemanticContract$ github.com/ShaulLavo/brine/internal/cli
experiment|| test -race -count=1 -timeout 1m ./...
EXPECTED

host="$scratch/host-race"
cat > "$scratch/fake-host.test" <<'MOCK'
#!/usr/bin/env bash
set -eu
names=(TestHostRaceFixtureA TestHostRaceFixtureB TestNewHostTest ExampleHostRace FuzzHostRace BenchmarkHostRace)
case $1 in
  -test.list) printf '%s\n' "${names[@]}" | command grep -E -- "${LIST_OVERRIDE:-$2}" || true ;;
  -test.paniconexit0) [[ $PWD == */internal/host && $0 == /* && $3 == 40m && $4 == -test.count=1 && $# == 6 ]]; printf '%s\n' "${names[@]}" | command grep -E -- "$6" >> "$RUN_LOG"; exit "${HOST_TEST_STATUS:-0}" ;;
  *) exit 2 ;;
esac
MOCK
chmod 700 "$scratch/fake-host.test"
check_commands test-race-host-build "$host" <<EXPECTED
root|| test -race -p 1 -c -o $host/host.test ./internal/host/
EXPECTED
diff -u - "$scratch/stdout" <<'EXPECTED'
host race shard 1: 2 tests
host race shard 2: 1 tests
host race shard 3: 1 tests
host race shard 4: 1 tests
EXPECTED
git -C "$root" rev-parse HEAD > "$host/source-head"
(cd "$host" && sha256sum host.test > host.test.sha256)
[[ $("$root/scripts/check.sh" test-race-host-shards) == '[1,2,3,4]' ]]
: > "$scratch/runs"
for shard in 1 2 3 4; do
  RUN_LOG="$scratch/runs" "$root/scripts/check.sh" test-race-host-shard "$shard" "$(realpath --relative-to="$root" "$host")" > /dev/null
done
printf '%s\n' ExampleHostRace FuzzHostRace TestHostRaceFixtureA TestHostRaceFixtureB TestNewHostTest > "$scratch/expected"
LC_ALL=C sort "$scratch/runs" | diff -u "$scratch/expected" -
if HOST_TEST_STATUS=17 RUN_LOG="$scratch/failed-run" "$root/scripts/check.sh" test-race-host-shard 1 "$host" > "$scratch/invalid" 2>&1; then
  echo 'Host race test failure must fail its shard.' >&2
  exit 1
else
  [[ $? == 17 ]]
fi
expect_host_race_refusal() {
  if RUN_LOG="$scratch/refused" "$root/scripts/check.sh" test-race-host-shard "$@" > "$scratch/invalid" 2>&1; then
    printf 'Host race shard must refuse: %s\n' "$*" >&2
    exit 1
  fi
}
for shard in 0 5 x 1x ''; do
  expect_host_race_refusal "$shard" "$host"
done
LIST_OVERRIDE=. expect_host_race_refusal 1 "$host"
printf '0000000000000000000000000000000000000000\n' > "$host/source-head"
expect_host_race_refusal 1 "$host"
git -C "$root" rev-parse HEAD > "$host/source-head"
printf '\n' >> "$host/host.test"
expect_host_race_refusal 1 "$host"
[[ ! -e $scratch/refused ]]
printf 'CI classifier and exact Go command tests passed.\n'
