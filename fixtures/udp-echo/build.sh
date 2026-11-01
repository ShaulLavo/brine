#!/usr/bin/env bash
set -euo pipefail
export PATH=$HOME/.local/share/mise/shims:$PATH
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GONOPROXY=none
if (( $# != 2 )); then
  echo 'Usage: fixtures/udp-echo/build.sh NEW_OUTPUT_DIRECTORY RELEASE' >&2
  exit 2
fi
fixture_dir=$(cd "$(dirname "$0")" && pwd)
repo_dir=$(cd "$fixture_dir/../.." && pwd)
output=$1
release=$2
[[ $output == /* && $output != / && ! -e $output ]]
[[ -n $release && ${#release} -le 64 && $release != *[!a-zA-Z0-9._-]* ]]
expected_go=$(sed -n 's/^go = "\([^"]*\)"/\1/p' "$repo_dir/mise.toml")
[[ $(go env GOVERSION) == "go$expected_go" ]]
keep=false
created=false
cleanup() {
  if [[ $created == true && $keep == false ]]; then rm -rf -- "$output"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -m 700 "$output"
created=true
cd "$repo_dir"
export CGO_ENABLED=0 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
for arch in amd64 arm64; do
  mkdir -p "$output/$arch"
  GOOS=linux GOARCH=$arch go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -buildid= -X main.release=$release" -o "$output/$arch/udp-echo-server" ./fixtures/udp-echo/server/cmd
  GOOS=linux GOARCH=$arch go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags='-s -w -buildid=' -o "$output/$arch/udp-echo-client" ./fixtures/udp-echo/client/cmd
  chmod 0555 "$output/$arch/udp-echo-server" "$output/$arch/udp-echo-client"
done
(cd "$output" && sha256sum amd64/udp-echo-* arm64/udp-echo-* > SHA256SUMS)
printf 'release=%s\ngo=%s\nsource=%s\n' "$release" "$expected_go" "$(git rev-parse HEAD)" > "$output/build.txt"
if ! git diff --quiet HEAD -- fixtures/udp-echo go.mod go.sum mise.toml || [[ -n $(git ls-files --others --exclude-standard -- fixtures/udp-echo) ]]; then
  printf 'source_dirty=true\n' >> "$output/build.txt"
fi
keep=true
