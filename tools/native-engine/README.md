# Native engine bundles

`prepare.py` verifies the upstream archive and shutdown patch before applying
the patch. It refreshes extracted file timestamps so a reused Cargo target
cannot treat older restored source as unchanged. File contents retain their
source and patch pins.

The native engine workflow builds amd64 and arm64 bundles on native runners.
Both architecture checks and the combined gate must pass before the main-only
publication job runs. Publication consumes those bundles without recompiling,
checks their complete checksum manifests, source commit and exact repository
pins, and copies the executable into the pinned original runtime image at
`/opt/spacetime/spacetimedb-standalone` with mode `0555`.

The image tag is the full source commit. The publication receipt records the
actual multiarch index digest and each executable's SHA-256. The workflow does
not publish a moving alias. Downloaded bundles can be checked again:

```sh
python3 tools/native-engine/verify_bundle.py package FULL_SOURCE_COMMIT
```

## Run the shutdown and reopen regression

`verify_lifecycle.py` accepts an existing executable, supplied v1 fixture Wasm,
their expected SHA-256 values, and a new results directory. It downloads and
compiles nothing. Supply a Linux network namespace containing only loopback.
For example, from a host with unprivileged user namespaces and OpenSSL:

```sh
unshare --user --map-root-user --net -- sh -c '
  ip link set dev lo up
  export TMPDIR=/work/tmp
  exec python3 tools/native-engine/verify_lifecycle.py "$@"
' sh /work/tmp/engine /work/tmp/module-v1.wasm /work/tmp/native-lifecycle-results \
  --binary-sha256 EXPECTED_EXECUTABLE_SHA256 --wasm-sha256 EXPECTED_WASM_SHA256
```

The runner creates fresh private signing keys and a disposable database, checks
confirmed `schema_marker` and `sequence_state` rows, requires SIGINT exit zero,
then immediately reopens and compares database identity, schema, program and
rows. Schema comparison orders only whole `Types`, `Tables` and
`ExplicitNames.entries` declarations. The pinned upstream schema converter
emits these collections from unordered maps (`crates/schema/src/def.rs`,
`From<ModuleDef> for RawModuleDefV10`). All values, duplicate counts, section
order and positional arrays remain part of equality, including Typespace,
product fields, index columns and reducer order.
It stops its owned processes and removes its keys and database on success,
failure or ordinary interruption. The private results directory retains a
scrubbed receipt and at most two 64 KiB logs. Its caller owns removal after
review. This regression does not establish managed restore or R2 delivery.

Check bundle refusals and archive timestamp restoration without compilation:

```sh
python3 tools/native-engine/verify_bundle_test.py
python3 tools/native-engine/prepare_test.py
python3 tools/native-engine/verify_lifecycle_test.py
```
