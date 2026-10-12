# P09-06 schema and publication core evidence

Status: Approved source unit. P09-06 and T39 remain unchecked. This unit supplies metadata comparison and private domain records; it performs no host mutation.

## Actual descriptor inputs

The pinned SpacetimeDB 2.11.0 standalone command is:

```sh
spacetimedb-standalone extract-schema /input/module.wasm --host-type wasm
```

It returns `{"V10": ...}` through the same `RawModuleDefV10::from(ModuleDef)` conversion as schema-v10 metadata GETs. Extraction runs guest `_initialize`, preinit and `__describe_module__` code. It does not run the application initialization reducer. That distinction requires containment, not trust in a metadata-only command name.

All six final path-remapped fixture modules from source `32af9fce211ddf9d42775c1b0b05270a930c4dcc` were extracted twice. The [descriptor pins](../../internal/spacetimedb/testdata/descriptor-pins.json) record exact Wasm, descriptor and canonical schema SHA-256 values. The retained JSON files are actual native output, including lowercase hex default bytes. Tests verify those content hashes and canonical semantics. Base metadata equals the qualified before/restart API schema.

The official amd64 executable SHA-256 was `71b5cff71ad5f8d8b3f66292a0ce88f99877454ef465744246c22204d0a72729`. Descriptor qualification used the helper built at default-wire repair source `dfcd2ddd826ee5708a88aa8a29a5cf85457d8845`, SHA-256 `1801492fa8da972d9c0352dc4cdac4ad9f2b24cee588b4c6b83cb8986a8bf3f3`. That repair subsequently merged through PR #118. Helper build provenance is the repair source, not its merge commit.

Each inspector had 512 MiB memory, no swap, 48 tasks, one CPU, a 20-second unit limit, 15-second native wait and 1 MiB output ceiling. Its private sandbox exposed no source tree, home or keys and had only loopback networking. Original module hashes remained unchanged. All twelve inspector units, processes, jobs and cgroups were independently absent after extraction. These results qualify the command and metadata profile. They do not implement production protected extraction or prove publication, queries or preserved data.

## Conservative comparison

`DecodeDescriptorV10` accepts only the strict V10 envelope and reuses the existing bounded canonicalizer. There is no second schema renderer, generated-code parser or trusted sidecar. `CompareSchemas` operates on immutable expanded canonical schemas.

| Base v1 to candidate | Decision and required evidence |
| --- | --- |
| Same schema with changed code | Staged validation, guest review and preserved-data verification |
| Additive | Staged validation plus new-table data proof |
| Default column | Staged validation plus old-column projection and U8 default-value proof |
| Destructive | Separate operator procedure |
| Access change | Separate operator procedure |
| Observer hooks | Staged validation plus callable/lifecycle effects qualification |

Removal, rename, product order/type changes, existing keys/indexes/constraints/sequences/defaults and existing-name remapping cannot become additive changes through a review label. Unqualified defaults refuse. Callable, lifecycle and scheduler changes require qualified effect review. The strongest decision survives mixed changes, and returned arrays cannot weaken retained requirements.

Independent review reproduced an existing-index rename using the unchanged real v1 source. Adding an explicit name for that existing index originally produced no change. The repaired comparison requires an operator procedure. Separate tests cover implicit table/function mapping additions, default changes, alias retargeting, mixed removal/default changes, native access changes and all shared limits.

## Private publication records and retention

`internal/datapublish` has closed recovered-empty and captured-history source arms. Empty creation names an owned allocation and recovered config/signing bundle without inventing native history or database identity. Initialized updates bind the exact point, retained fence/preparation owner, source witness/stop/checkpoint references and native tuple. The current named source contract accepts only qualified final v1/additive/default-column artifact/schema pairs. It is a finite expected contract, not generic all-acknowledgements authority.

Plans retain the existing personal requester, current facts, candidate Wasm/native-program/schema, guest and recovery-release review references, private preflight references, derived requirements and a UTC lifetime of at most fifteen minutes. Private decoding requires bound opaque source/candidate schemas and rederives the requirements. Secret references stay in the app and are distinct across roles. Numeric version numbers may match, and key names remain operator-selected. Protected role lookup must authenticate the exact mapping before any effect.

The cursor binds a separate apply operation and consumer claim while preserving preparation ownership. An unknown publication cannot be reissued or skip data verification. Matching observations may advance the retained attempt; known nonperformance remains consumed. Creation adopts native identity only after private publication observation. Disposal precedes the external final-transition receipt.

Independent review coherently truncated an unknown-publication cursor back to its preflight prefix and reopened publication under the same consumer claim. Raw replay alone cannot prove monotonic history. The repaired decoder therefore requires the exact record SHA-256 loaded independently from protected retained state before parsing/replay. Future store code must retain record and hash atomically and compare-and-swap the expected head. It must never compute that expected hash from request-supplied cursor bytes. Tests separately check coherent truncation against the old retained hash and malformed records against matching test hashes.

Focused descriptor/domain tests, focused race tests, vet, structure, lint, formatting and diff checks passed before the review repairs. The final repaired head has its own immutable check run and PR CI. Complete required CI and independent final approval remain merge gates.

## Remaining effect and acceptance gates

The core has no executable path/URL/argv input, host effect runner, durable store claim or public command. A hash reference proves neither protected evidence nor ownership. The following composition remains required:

- Protected artifact extraction and independently authenticated role/review/preflight lookup.
- Exact fenced source preparation with source-state witness and independently verified remote replay.
- One durable consumer claim, authenticated cursor/proof retention and observational reconciliation.
- Contained staged publication with full preserved-data/new-table/default projection and effect proofs.
- Journaled bundle swap, successor epoch/runtime/control transaction and explained fence release.
- Existing jobs/host/store/dispatch integration, compatible recovery-release admission and physical Pi/S3/browser/reboot acceptance.

Exact temporary reachability allowances cover the unconnected core. Production integration must link the intended functions, remove their allowances and delete unused interfaces before full Phase 09 exit. No checkbox or production-readiness claim follows from these records.
