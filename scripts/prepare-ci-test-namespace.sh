#!/usr/bin/env bash
set -euo pipefail

if [[ ${GITHUB_ACTIONS:-} != true || ${RUNNER_ENVIRONMENT:-} != github-hosted || ${RUNNER_OS:-} != Linux ]]; then
  echo 'Namespace preparation is only allowed on ephemeral GitHub-hosted Linux runners.' >&2
  exit 2
fi

uid=$(id -u)
gid=$(id -g)
user=$(id -un)
if [[ $uid == 0 ]]; then
  echo 'Host tests must run as the original unprivileged runner user.' >&2
  exit 2
fi
subuid=$(awk -F: -v user="$user" '$1 == user { print $2; exit }' /etc/subuid)
subgid=$(awk -F: -v user="$user" '$1 == user { print $2; exit }' /etc/subgid)
if [[ ! $subuid =~ ^[0-9]+$ || ! $subgid =~ ^[0-9]+$ ]]; then
  echo 'The runner user needs subordinate UID and GID allocations for the host fixture.' >&2
  exit 1
fi

probe_namespace() {
  # Match TestMain's maps, including its original UID, and unshare's private mount propagation.
  unshare --user --mount --setuid=0 --setgid=0 \
    "--map-users=0:$subuid:1" "--map-users=$uid:$uid:1" \
    "--map-groups=0:$subgid:1" "--map-groups=$gid:$gid:1" /usr/bin/true
}

restriction=$(sysctl -n kernel.apparmor_restrict_unprivileged_userns 2>/dev/null || printf 'unavailable')
printf 'Namespace preflight: uid=%s gid=%s subuid=%s subgid=%s apparmor_restrict_unprivileged_userns=%s\n' \
  "$uid" "$gid" "$subuid" "$subgid" "$restriction"
if probe_namespace; then
  exit 0
fi
if [[ $restriction != 1 ]]; then
  echo 'Host fixture namespaces are unavailable; no supported CI prerequisite adjustment applies.' >&2
  exit 1
fi

# Ubuntu restricts capabilities inside unprivileged user namespaces. Only the disposable CI VM changes.
sudo -n sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
if [[ $(sysctl -n kernel.apparmor_restrict_unprivileged_userns) != 0 ]]; then
  echo 'The hosted runner did not apply the namespace prerequisite.' >&2
  exit 1
fi
probe_namespace
