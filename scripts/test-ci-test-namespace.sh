#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/brine-namespace-tests-XXXXXX")
trap 'rm -rf "$scratch"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
mkdir "$scratch/bin"

cat > "$scratch/bin/id" <<'MOCK'
#!/usr/bin/env bash
set -eu
case $1 in
  -u) printf '%s\n' "${NAMESPACE_UID:-1001}" ;;
  -g) echo 1002 ;;
  -un) echo runner ;;
  *) exit 2 ;;
esac
MOCK
cat > "$scratch/bin/awk" <<'MOCK'
#!/usr/bin/env bash
set -eu
[[ $1 == -F: && $2 == -v && $3 == user=runner ]]
case $5 in
  /etc/subuid) printf '%s\n' "${NAMESPACE_SUBUID-100000}" ;;
  /etc/subgid) echo 200000 ;;
  *) exit 2 ;;
esac
MOCK
cat > "$scratch/bin/unshare" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf 'unshare %s\n' "$*" >> "$NAMESPACE_LOG"
[[ $* == '--user --mount --setuid=0 --setgid=0 --map-users=0:100000:1 --map-users=1001:1001:1 --map-groups=0:200000:1 --map-groups=1002:1002:1 /usr/bin/true' ]]
[[ ${NAMESPACE_PROBE:-restricted} != blocked ]] || exit 17
[[ ${NAMESPACE_PROBE:-restricted} == allowed || $(cat "$NAMESPACE_STATE") == 0 ]] || exit 17
MOCK
cat > "$scratch/bin/sysctl" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf 'sysctl %s\n' "$*" >> "$NAMESPACE_LOG"
case "$*" in
  '-n kernel.apparmor_restrict_unprivileged_userns')
    [[ $(cat "$NAMESPACE_STATE") != unavailable ]] || exit 1
    cat "$NAMESPACE_STATE" ;;
  '-w kernel.apparmor_restrict_unprivileged_userns=0')
    [[ ${NAMESPACE_INEFFECTIVE:-0} == 1 ]] || echo 0 > "$NAMESPACE_STATE" ;;
  *) exit 2 ;;
esac
MOCK
cat > "$scratch/bin/sudo" <<'MOCK'
#!/usr/bin/env bash
set -eu
printf 'sudo %s\n' "$*" >> "$NAMESPACE_LOG"
[[ ${NAMESPACE_SUDO_FAIL:-0} != 1 ]] || exit 19
[[ $1 == -n ]]
shift
exec "$@"
MOCK
chmod 700 "$scratch/bin/"*
export PATH="$scratch/bin:$PATH"
export GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted RUNNER_OS=Linux
export NAMESPACE_LOG="$scratch/commands" NAMESPACE_STATE="$scratch/state"

check_namespace() {
  local want=$1 state=$2 got=0
  : > "$NAMESPACE_LOG"
  printf '%s\n' "$state" > "$NAMESPACE_STATE"
  bash "$root/scripts/prepare-ci-test-namespace.sh" > "$scratch/output" 2>&1 || got=$?
  if [[ $got != "$want" ]]; then
    cat "$scratch/output" >&2
    printf 'Namespace setup returned %s, expected %s.\n' "$got" "$want" >&2
    exit 1
  fi
}

expect_no_adjustment() {
  if grep -q '^sudo ' "$NAMESPACE_LOG"; then
    echo 'Namespace setup must not change this runner policy.' >&2
    exit 1
  fi
}

for guard in 'GITHUB_ACTIONS=false' 'RUNNER_ENVIRONMENT=self-hosted' 'RUNNER_ENVIRONMENT=' 'RUNNER_OS=macOS' 'NAMESPACE_UID=0' 'NAMESPACE_SUBUID=' 'NAMESPACE_SUBUID=invalid'; do
  case $guard in
    NAMESPACE_SUBUID=*) want=1 ;;
    *) want=2 ;;
  esac
  export "${guard?}"
  check_namespace "$want" 1
  [[ ! -s $NAMESPACE_LOG ]]
  unset "${guard%%=*}"
  export GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted RUNNER_OS=Linux
done

NAMESPACE_PROBE=allowed check_namespace 0 1
[[ $(cat "$NAMESPACE_STATE") == 1 ]]
[[ $(grep -c '^unshare ' "$NAMESPACE_LOG") == 1 ]]
expect_no_adjustment
check_namespace 0 0
expect_no_adjustment
NAMESPACE_PROBE=allowed check_namespace 0 unavailable
expect_no_adjustment

check_namespace 0 1
[[ $(cat "$NAMESPACE_STATE") == 0 ]]
[[ $(grep -c '^unshare ' "$NAMESPACE_LOG") == 2 ]]
[[ $(grep -c '^sudo -n sysctl -w kernel.apparmor_restrict_unprivileged_userns=0$' "$NAMESPACE_LOG") == 1 ]]
[[ $(grep '^unshare ' "$NAMESPACE_LOG" | sort -u | wc -l) == 1 ]]

for state in 0 unavailable 2; do
  NAMESPACE_PROBE=blocked check_namespace 1 "$state"
  [[ $(grep -c '^unshare ' "$NAMESPACE_LOG") == 1 ]]
  expect_no_adjustment
done
NAMESPACE_SUDO_FAIL=1 check_namespace 19 1
[[ $(grep -c '^unshare ' "$NAMESPACE_LOG") == 1 ]]
NAMESPACE_INEFFECTIVE=1 check_namespace 1 1
[[ $(grep -c '^unshare ' "$NAMESPACE_LOG") == 1 ]]
NAMESPACE_PROBE=blocked check_namespace 17 1
[[ $(grep -c '^unshare ' "$NAMESPACE_LOG") == 2 ]]
printf 'Hosted namespace setup tests passed.\n'
