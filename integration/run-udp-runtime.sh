#!/usr/bin/env bash
set -euo pipefail
[[ ${BRINE_UDP_RUNTIME_TEST:-} == 1 && $EUID != 0 ]]
work=$(cd "$(dirname "$0")" && pwd)
unit="$HOME/.config/containers/systemd/p08-udp-runtime.container"
image='docker.io/nginxinc/nginx-unprivileged@sha256:7377697a821c131a924a7105fafbe7414db4e9fcc77a6f08f776f33f141ec3f8'
[[ ! -e $unit && ! -e $work/unit.sha256 ]]
[[ -z $(podman --remote=false ps -a --format '{{.Names}}' | grep -x systemd-p08-udp-runtime || true) ]]
created=()
for dir in .config .config/containers .config/containers/systemd .local .local/share .local/share/brine .local/share/brine/quadlet .local/share/brine/quadlet/staging; do
  [[ -e $HOME/$dir ]] || created+=("$HOME/$dir")
done
podman --remote=false images --no-trunc --format '{{.ID}}' > "$work/images.before"
cleanup() {
  local result=$? digest id i status
  if [[ -e $unit ]]; then
    digest=$(sha256sum "$unit")
    if [[ -s $work/unit.sha256 && ${digest%% *} == "$(cat "$work/unit.sha256")" ]]; then
      systemctl --user stop p08-udp-runtime.service || result=1
      rm -- "$unit"
      systemctl --user daemon-reload || result=1
    else
      echo 'Unknown fixture unit bytes retained for inspection.' >&2
      return 1
    fi
  fi
  if podman --remote=false container exists systemd-p08-udp-runtime; then
    podman --remote=false rm systemd-p08-udp-runtime || result=1
  else
    status=$?
    [[ $status == 1 ]] || result=1
  fi
  id=''
  if podman --remote=false image exists "$image"; then
    id=$(podman --remote=false image inspect --format '{{.Id}}' "$image") || result=1
  else
    status=$?
    [[ $status == 1 ]] || result=1
  fi
  if [[ -n $id ]] && ! grep -Fxq "$id" "$work/images.before"; then
    podman --remote=false rmi "$id" || result=1
  fi
  [[ ! -e $unit ]] || result=1
  [[ -z $(ss -H -ltn 'sport = :20431') ]] || result=1
  [[ -z $(ss -H -lun 'sport = :24431') ]] || result=1
  for ((i=${#created[@]}-1; i>=0; i--)); do
    [[ ! -e ${created[i]} ]] || rmdir -- "${created[i]}" || result=1
  done
  echo "Outer teardown result=$result; fixture unit/container/socket cleanup checked."
  return "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
export BRINE_UDP_RUNTIME_WORK="$work"
timeout --signal=TERM --kill-after=30 300 "$work/udp-runtime.test" -test.v -test.run '^TestIsolatedUDPRuntime$' -test.timeout 4m
