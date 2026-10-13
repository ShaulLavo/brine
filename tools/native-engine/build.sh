#!/usr/bin/env bash
set -euo pipefail
if [[ $# != 2 ]]; then
  echo 'usage: build.sh PREPARED_UPSTREAM OWNED_TARGET' >&2
  exit 64
fi
source_root=$1
target_root=$2
test "$(rustc --version | cut -d ' ' -f 2)" = 1.96.1
test -f "$source_root/Cargo.lock"
mkdir -p "$target_root"
export CARGO_TARGET_DIR="$target_root" CARGO_INCREMENTAL=0 CARGO_BUILD_JOBS=2
cd "$source_root"
cargo build --locked --release -p spacetimedb-standalone -j 2
sha256sum "$target_root/release/spacetimedb-standalone"
