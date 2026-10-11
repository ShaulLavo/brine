# Pinned runtime probes

This directory runs finite experiments on new private local fixtures. It does not enroll a host, admit a public SpacetimeDB spec, grant a startup permit, or write an exact restore receipt.

`internal/spacetimedb` owns the production schema canonicalizer and fixed private metadata observer. The Go tool calls that package. The Python probe has no independent canonical renderer. Its module and official confirmed SDK client come from the separately built `fixtures/spacetimedb-app` inputs; this directory does not copy or build a second module.

## Build the shared helper

Build into disposable scratch, outside the repository:

```sh
go build -o /work/tmp/spacetime-runtime ./tools/spacetime-runtime
sha256sum /work/tmp/spacetime-runtime
```

Canonicalize a bounded raw schema-v10 document with stdin:

```sh
/work/tmp/spacetime-runtime --mode canonical < schema-v10.json
```

The output binds `spacetimedb-v10-expanded-v2`, SHA-256, a derived schema marker, and the canonical document. Unknown sections, recursive references, duplicate declarations, malformed JSON, and resource-budget overflow refuse. Type, table, reducer, schedule, lifecycle, and explicit-name sets are ordered canonically. Product columns, enum variants, reducer parameters, index column lists, and default-value bytes retain their order. Positional type references are expanded before Typespace ordering.

`--mode observe` reads this private JSON shape on stdin:

```json
{
  "host": "127.0.0.1",
  "port": 3000,
  "database_identity": "64_lowercase_native_hex_characters",
  "publisher_identity": "64_lowercase_native_hex_characters",
  "names": ["fixture"],
  "publisher_credential": "supplied_privately_on_stdin"
}
```

Native database info represents identities as `{"__identity__":"0x..."}`. The input uses those exact native values after removing `0x`. It uses the expected owning publisher, not an unrelated newly issued identity. Credentials never appear in argv, returned observations, or error text. The helper permits literal loopback HTTP only, disables redirects and proxies, and issues exactly the qualified info, names, environment, schema-v10, and environment GET requests. It does not use SQL or WS. Endpoint validation establishes a location only; the future host adapter establishes actual runtime ownership and retains control serialization.

The current module identity comes from the two matching environment responses, not `initial_program`. It preserves the exact native API u256 wire hex. The pinned runtime hashes program bytes with Keccak-256. Its digest display reverses the bytes relative to that JSON wire identity. Neither is a Wasm SHA-256 digest.

## Run the optional native gate

The gate requires Linux, OpenSSL, Bubblewrap, the official pinned standalone binary, Bun 1.4.2, an explicitly hashed module, a matched official SDK client, and an explicitly hashed shared helper. It downloads nothing and does not invoke the upstream CLI or change its global configuration.

Provide inputs from the fixture's retained artifact manifest and clients:

```sh
python3 tools/spacetime-runtime/probe.py \
 --runtime /private/tools/spacetimedb-standalone \
 --module /private/artifacts/module-v1.wasm \
 --module-sha256 MODULE_SHA256_FROM_MANIFEST \
 --helper /work/tmp/spacetime-runtime \
 --helper-sha256 HELPER_SHA256 \
 --client /private/clients/v1/drill.ts \
 --bun /private/tools/bun \
 --scratch /work/tmp/runtime-probes
```

The runtime hash must match the architecture entry in `pins.json`. The probe creates one new mode-0700 directory, fresh fixture signing keys, and an ordinary user distinct from the publisher. Source and verification listeners bind loopback. The probe checks that its actual process owns the listener before mutations. It creates a single module database, authorizes its fixture writer, and records only explicitly confirmed SDK evidence.

The core gate archives an online native finalized snapshot, required log suffix, and bounded active prefix using the stopped bootstrap's immutable metadata checkpoint. It discovers the replica identity. The completed-only replay may be older. The active-head replay must match the source's recorded confirmed rows and current module/control/schema. It records SIGINT exit and forced-kill status separately. Even a clean exit does not issue an exact-point receipt.

The initialized-start experiment copies a stopped source into a fresh Bubblewrap user/PID/network namespace. The source is absent from mounts, only loopback exists, and signing material is mounted read-only. Withholding `program-bytes` from the accessible private copy forces current persisted `st_module` recovery. Intact history must match. Missing replica history with bootstrap bytes intentionally demonstrates false fresh reconstruction; withholding those bytes must refuse. Original bytes and process absence are checked. The verifier can mutate only its disposable copy, and every verifier is gone before the result completes.

The probe's stdout is scrubbed JSON. Source logs, fixture keys, user tokens, and failed copies remain in private scratch for inspection. Cleanup stops only its own processes. The operator removes only the generated directory after preserving needed evidence. No real host, service, Pi, or S3 storage is used by default CI. Running this on another host requires that host's separate fixture authorization.

The wider physical receipts in [P09 runtime evidence](../../docs/evidence/p09-runtime.md) cover torn tails, metadata drift, shutdown failures, JWT ingress, and OCI behavior. This core gate does not repeat every physical case or replace the later Brine/R2 drill. The exact-state native-reader and production fence/startup composition remain later integration work.

## Check without a real runtime

```sh
bash scripts/check-spacetime-runtime.sh
go test ./internal/spacetimedb ./tools/spacetime-runtime
```

Default CI runs these syntax, pin, typed-observer, canonicalization, and refusal checks without VPS access or credentials.
