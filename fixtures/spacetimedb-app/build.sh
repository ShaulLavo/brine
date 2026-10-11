#!/usr/bin/env bash
set -euo pipefail
fixture_dir=$(cd "$(dirname "$0")" && pwd)
repo_dir=$(cd "$fixture_dir/../.." && pwd)
: "${BRINE_FIXTURE_BUILD_ROOT:?set an empty build directory}"
build_root=$BRINE_FIXTURE_BUILD_ROOT
mkdir -p "$build_root/artifacts" "$build_root/runtime" "$build_root/clients"

archive_sha=fe4645f3abc96a5386ebe98879bba0bea2d0bf56ad7612c322e36a7b93ba04d0
archive="$build_root/runtime/spacetime-2.11.0-amd64.tar.gz"
if [[ -n ${BRINE_SPACETIMEDB_ARCHIVE:-} ]]; then
  cp "$BRINE_SPACETIMEDB_ARCHIVE" "$archive"
elif [[ ! -f $archive ]]; then
  curl --fail --location --proto '=https' --max-time 180 --retry 2 \
    https://github.com/clockworklabs/SpacetimeDB/releases/download/v2.11.0/spacetime-x86_64-unknown-linux-gnu.tar.gz -o "$archive"
fi
printf '%s  %s\n' "$archive_sha" "$archive" | sha256sum -c -
tar -xzf "$archive" -C "$build_root/runtime"
cli="$build_root/runtime/spacetimedb-cli"
export SOURCE_DATE_EPOCH=0
cargo_home=${CARGO_HOME:-$HOME/.cargo}
rustup_home=${RUSTUP_HOME:-$HOME/.rustup}
export CARGO_ENCODED_RUSTFLAGS="--remap-path-prefix=$cargo_home=/brine-cargo"$'\x1f'"--remap-path-prefix=$rustup_home=/brine-rustup"$'\x1f'"--remap-path-prefix=$fixture_dir/module=/brine-module"
for variant in v1 additive destructive observer-hooks default-column access-change; do
  args=()
  if [[ $variant != v1 ]]; then args=(--features "$variant"); fi
  export CARGO_TARGET_DIR="$build_root/cargo-$variant"
  cargo +1.96.1 build --locked --manifest-path "$fixture_dir/module/Cargo.toml" --target wasm32-unknown-unknown --release "${args[@]}"
  cp "$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/brine_recovery_fixture.wasm" "$build_root/artifacts/module-$variant.wasm"
done
export CARGO_TARGET_DIR="$build_root/cargo-repro-v1"
cargo +1.96.1 build --locked --manifest-path "$fixture_dir/module/Cargo.toml" --target wasm32-unknown-unknown --release
cmp "$build_root/artifacts/module-v1.wasm" "$CARGO_TARGET_DIR/wasm32-unknown-unknown/release/brine_recovery_fixture.wasm"

for variant in v1 additive default-column; do
  client="$build_root/clients/$variant"
  mkdir -p "$client"
  cp "$fixture_dir"/client/*.ts "$fixture_dir/client/package.json" "$fixture_dir/client/bun.lock" "$fixture_dir/client/tsconfig.json" "$client/"
  "$cli" --config-path "$build_root/runtime/cli.toml" generate \
    --bin-path "$build_root/artifacts/module-$variant.wasm" --lang typescript --out-dir "$client/module_bindings" --no-config -y
  python3 - "$client/module_bindings" <<'PYBIND'
import pathlib, sys
for path in pathlib.Path(sys.argv[1]).rglob('*.ts'):
    path.write_text(path.read_text().rstrip() + '\n')
PYBIND
  bun install --cwd "$client" --frozen-lockfile
  if [[ $variant == v1 ]]; then diff -ru "$fixture_dir/client/module_bindings" "$client/module_bindings"; fi
  "$client/node_modules/.bin/tsc" -p "$client"
  bun build "$client/browser.ts" --target browser --minify --outfile "$build_root/artifacts/browser-$variant.js"
done
cp "$build_root/artifacts/browser-v1.js" "$fixture_dir/web/browser.js"
(cd "$repo_dir" && go test ./fixtures/spacetimedb-app && go vet ./fixtures/spacetimedb-app)
(cd "$repo_dir" && go build -trimpath -o "$build_root/runtime/spacetime-runtime" ./tools/spacetime-runtime)
for arch in amd64 arm64; do
  (cd "$repo_dir" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags='-s -w' -o "fixtures/spacetimedb-app/bin/$arch/spacetimedb-app" ./fixtures/spacetimedb-app)
done
(cd "$build_root/artifacts" && sha256sum *.wasm browser-*.js > SHA256SUMS)
python3 "$fixture_dir/manifest.py" "$build_root" "$fixture_dir"
