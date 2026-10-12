# Native passive state reader

This tool reads a private SpacetimeDB 2.11.0 replica copy without ModuleHost, init or client hooks, scheduled reducers, SQL, a listener or external program storage. It reports the recovered current program, committed offset and persisted table state. It does not grant startup, capture, publish or restore authority.

The operation owner creates a fresh copy while holding the source locks and fences, excludes the original from the verifier, checks both content seals and disposes the verifier. Native segment readers open files read-write to call `sync_data`, so only the disposable copy may be writable.

## Protocol

The executable accepts no arguments. It reads one JSON request, at most 8 KiB, from stdin and requires `/copy/replica` inside a read-only root filesystem with isolated networking. It rejects foreign mounts, inherited non-stdio descriptors, credential environment variables, symlinks, hard links, special files, foreign owners and an unrestricted host invocation. The launcher must create filesystem and PID namespaces, give the helper only the private replica mount, clear its environment and impose process and memory limits. The helper's filesystem checks supplement that launcher contract. They cannot distinguish an original deliberately mounted as `/copy` from a correctly materialized copy.

Standard OCI runtime masks at `/sys/firmware`, `/sys/devices/virtual/powercap` and numbered CPU `thermal_throttle` directories are accepted only as read-only, empty tmpfs mounts. Writable, populated, non-tmpfs and other extra mounts still refuse.

Podman's `/run/.containerenv` marker is accepted only as a file bind with `ro,nosuid,nodev,noexec`, an empty regular file, mode `0644`, container UID zero and one link. Visible symlinks, other file types, populated or writable markers and other `/run` mounts refuse. The launcher suppresses Podman's additional writable tmpfs and passwd/group mounts.

`format` must equal `brine-native-reader-v1`. `expected_database_identity` and `expected_owner_identity` are equality constraints, not observed evidence. Unknown, repeated, absent or mistyped request fields refuse. `history` is either `{"kind":"logs"}` or `{"kind":"snapshot","offset":N,"replica_id":N}`. Snapshot mode requires exactly the selected complete snapshot, its verified native identity and a connected log suffix. The suffix may be empty when the selected snapshot itself supplies the recovered head. The required `clog` directory must then contain no entries. Log mode requires nonempty history beginning at zero.

On success, exit status is zero and `outcome` is `observed`. On refusal, exit status is 70 and a bounded object identifies the refusal code when the output pipe permits it. A killed process, partial output, unexpected status, invalid JSON or missing evidence must refuse. A refusal is never a restore receipt.

Allocator, watchdog and cooperative errors share one refusal writer. A second fatal thread cannot append another JSON object or interrupt the selected refusal writer. Once an observed response starts, a deadline still aborts the process with status70, including a blocked output pipe, without appending a refusal. Only complete output with status zero is successful evidence.

### Observed fields

| Field | Native source |
| --- | --- |
| `observed_database_identity`, `observed_owner_identity` | `RelationalDB::metadata()` reads persisted `st_module` columns. Constructor-echo accessors are not used. |
| `current_program_digest` | Keccak-256 digest recomputed from persisted current program bytes. |
| `current_program_hash` | Native JSON API u256 wire representation, the digest bytes reversed. |
| `current_program_sha256`, `program_bytes` | Actual persisted program bytes. They are separate from the native runtime hash. |
| `committed_offset` | Complete physical framing and continuity, compared with the recovered transaction offset. With an empty suffix, the actual reconstructed and consumed snapshot supplies the offset. |
| `snapshot` | Actual decoded snapshot database, replica ID and offset, or null in log mode. |
| `persisted_connected_clients` | Clients retained in recovered native state. This is not proof of a live writer's absence. |
| `tables`, `state_blake3` | Every recovered table, including persisted system catalog rows. |
| `tables[].table_type` | Actual native `TableSchema::table_type`, exactly `system` or `user`. No table-name or ID inference. |
| `confirmed_pairs_projection` | Fixed named-column facts read through the recovered Internal transaction, or null when required tables or exact column positions/names/types do not fit. |

Bare logs contain no replica ID. Logical database names and control aliases are also absent. The caller must bind those through its sealed control checkpoint. Table state hashes are not `CanonicalSchemaV10` hashes. The runtime package owns canonical module/schema observation.

`framing` is `complete-v1` for nonempty complete logs. A complete selected snapshot with an empty log suffix reports `snapshot-only-v1`, zero actual `input.commits`, and `committed_offset` equal to the observed snapshot offset. It does not invent a log transaction or a placeholder offset. Missing, torn, corrupt, orphaned or header-only log input cannot select this arm.

Replica selectors require an explicit u64 field and equality with the actual reconstructed snapshot. Zero is valid in the pinned native snapshot format. The standalone allocator normally issues a database ID before its first replica ID, so an ordinary new standalone replica starts above zero. That allocation behavior does not make zero corrupt in a native snapshot. Missing or mismatched selectors still refuse.

### Fixed passive projection

`confirmed_pairs_projection` and every table's `table_type` are required output fields. The projection's format is `brine-confirmed-pairs-projection-v1`. It contains actual table IDs/names and independently extracted rows for `recovery_left`, `recovery_right`, `schema_marker`, `publisher` and `sequence_state`, plus `fixture_note` when present. Pair tables require exactly `sequence:U64, marker:String, commit_time:Timestamp, scheduled:Bool`, optionally followed by `rating:U8`. Their layout fact is `base-v1` or `rating-v1`; every row has an explicit nullable rating. Timestamps report signed native microseconds. Publisher identities use the native Identity hex representation, with its observed U256 byte order.

The reader does not pair rows, require continuity or fixed markers, compare publisher values with ownership, qualify a module, or issue an eligibility flag. The SpacetimeDB domain package applies those rules against the actual program and registered schema. No caller SQL, profile, row list or Wasm registry enters the reader. Native module accessor aliases may exist; extraction still requires the exact stored column names and positions, and the complete catalog commitment binds their persisted aliases.

Projection values are charged in addition to table commitments against the same row/state ceilings before collection. Each projected row's JSON bytes are counted before collection, and the complete output remains bounded by the existing output ceiling. Oversized projections refuse. The Go decoder checks strict required fields, table identity/name/count bindings and resource totals, then owns canonical bytes and SHA-256 for the complete decoded result. Returned bytes, rows and optional values are detached. Decoding or hashing these facts does not prove source ownership or authorize an operation.

### State hash format

`evidence_algorithm` is `bsatn-row-digest-multiset-blake3-v1`. Each row hash contains the domain `brine-native-row-v1\0`, the encoded byte length as little-endian u64, and the row's ordered native BSATN bytes. The helper counts and measures a row before serialization and stores only its 32-byte digest.

Each table hash contains `brine-native-table-v1\0`, its little-endian u32 ID, name length and name, row count, total BSATN bytes, then sorted row digests. All lengths and counts use little-endian u64. Duplicate rows remain duplicated. Each value's field and array order stays inside its BSATN digest.

The state hash contains `brine-native-state-v1\0`, the observed database and owner identity bytes, native program digest bytes, committed offset and table count, then each fixed-width lowercase table hash in ascending table-ID order. No Debug renderer or JSON object order determines this hash.

## Resource limits

Every request supplies all `limits` fields. Limits may be reduced from these hard ceilings:

| Limit | Ceiling |
| --- | --- |
| Files, directory depth | 8,192 files, depth 8; directory entries also bounded |
| Stored bytes, file bytes | 256 MiB total, 64 MiB per file |
| Expanded bytes, expanded file bytes | 256 MiB total, 64 MiB per file |
| Commit bytes, commit count | 8 MiB payload, 1,000,000 commits |
| Tables, rows | 512 tables, 100,000 rows across all tables |
| Row bytes, state bytes | 8 MiB per row, 128 MiB total BSATN |
| Program bytes | 8 MiB; the encoded program record must fit before cloning |
| Output bytes | 1 MiB, minimum 256 bytes |
| Rust memory, single allocation | 384 MiB live allocations, 64 MiB per allocation |
| Deadline | 60 seconds |

Stored and expanded totals are checked while consuming files. Zstd windows are limited to 8 MiB before native replay. Declared commit payload lengths must fit both available bytes and the configured ceiling before native allocation. Table and program-record counts are checked before schema/program collection. Rows are counted before digest collection. Output is fully bounded before writing.

A global Rust allocator enforces configured allocation ceilings before calling the system allocator. Its refusal writes a small static message without allocation and exits 70. Native C allocations remain constrained by decompressor preflight, a 1 GiB process address-space ceiling and the launcher's memory limit. Process OOM earns no evidence. Watchdogs enforce wall time, including blocked output, without blocking on a full output pipe.

## Build and distribution

`pins.json` fixes the upstream commit, source archive SHA-256, Rust version and actual multi-platform musl builder manifests. `prepare.py NEW_DIRECTORY` verifies the source archive, reduces workspace and development-only manifests, copies this tool and its lock, and leaves all upstream Rust code unchanged. Native tool sources receive fresh copy times so a shared Cargo target cannot treat different earlier source bytes as an unchanged package. Local verification also clears only the owned reader package and checks expected test names and output fields before accepting its artifact. `Cargo.lock` retains exact upstream runtime dependency versions.

The native reader workflow builds on matching amd64 and arm64 runners with the pinned Rust 1.96.1 Alpine builder. It checks the static ELF target, runs native tests and exercises the contained executable. Artifact bundles retain executable SHA-256, source head, source pins, license and CLI evidence. The main-only publisher uses those tested bytes to publish an immutable GHCR image. An enrolled host needs the verified executable, not Cargo or a compiler.

Published hashes and platform compatibility must come from completed workflow receipts. A workstation glibc build is development evidence only.

Native compilation layers use architecture-specific GHA cache scopes bound to the pinned builder and upstream source. Source and lockfile contents determine layer keys. The separate `native-tests` stage is forced fresh on every workflow run. Normal, replica-zero and fixed base/additive/default-column fixtures receive fresh contained CLI checks. Cached layers never replace ELF, checksum, test or publication verification.

## Coverage limits

Native `Complete` alone is insufficient: a short final commit header can be silently accepted. The helper additionally checks physical framing, CRC and continuity. Missing initialized history, torn input and incompatible snapshots refuse.

Clean removal of a whole complete transaction can still yield a valid older head. Exact mutation therefore requires an independently sealed source witness, final source state and writer exclusion through the operation. This reader does not promise survival of lost acknowledgements or unconfirmed RAM effects. Ordinary recoverable points and exact pre-mutation authority remain different evidence.
