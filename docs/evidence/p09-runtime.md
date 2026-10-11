# P09 runtime contract and evidence

P09-02 delivers the reproducible local gate, qualified canonical schema and metadata primitives, and the following pinned runtime contract. Managed SpacetimeDB spec, capture, and startup admission remain disabled. P09-02 remains unchecked until its complete acceptance is integrated; these tests do not supply a production startup permit or exact restore receipt.

## Artifact and scope

The official standalone version is 2.11.0. Module and client SDK are 2.11.0, the Rust fixture toolchain is 1.96.1, and Bun is 1.4.2. [Pinned metadata](../../tools/spacetime-runtime/pins.json) records the independently verified OCI index, both architecture manifests, native archives, executed binaries, and UID/GID 1000. Both Linux native architectures ran actual Rust fixtures. Only arm64 OCI execution is claimed.

The initial official arm64 rootless keep-id container preparation took 523.67 seconds while mapping image ownership. Its entrypoint became standalone PID 1. SIGINT through `podman stop` returned container exit 0 and privately replayed its explicitly confirmed row. The image is about 907 MB compressed and 2.54 GB uncompressed on arm64. These are setup costs, not a game capacity estimate.

The retained repository gate executed on official native amd64 with an explicitly pinned fixture module `f9d9510db3d75c78cd77ff4816f5af4b97c0af0499cad6c6ec0ebf4072d5c811`. Its shared Go helper used canonicalizer `spacetimedb-v10-expanded-v2`.

| Repository gate | Actual result |
| --- | --- |
| Explicitly confirmed source | 180 paired sequence/marker/transaction-time rows |
| Completed snapshot and closed logs | Valid earlier prefix of 135 rows |
| Same capture with bounded active prefix | All 180 source rows, identical module/control/schema |
| Original stopped tree, bootstrap bytes withheld in private verifier | Current persisted module/control/schema matched |
| Historical control, all replicas missing, bootstrap available | Native falsely reconstructed matching initial module/schema |
| Same missing history, bootstrap bytes withheld | Refused |
| Source preservation and isolation | Source seal unchanged, source absent from verifier, loopback-only network, all verifier processes gone |
| Source SIGINT | Exit 1, separately unknown; no exact-point candidate or receipt |

The [manual gate](../../tools/spacetime-runtime/README.md) takes explicit module/client/runtime/helper inputs and does not duplicate fixture code. The new Go primitives were also exercised by the fixture lane against real source and private replay metadata. Their unit fixtures are actual same-program before/restart schema-v10 payloads, with actor data and credentials excluded.

## Online capture and control checkpoint

Routine archives use a finalized native snapshot, all required subsequent history, and an explicitly bounded active prefix. A closed path is not immutable. The physical arm64 probe observed one completed segment shrink from 33,051 to 6,800 bytes after native maintenance. Capture validates owned file identity and exact bytes, bounds expansion, seals member hashes, and independently replays. A race or incomplete capture refuses without a receipt.

The bounded arm64 probe explicitly confirmed 1200 rows. Closed-only copies recovered 1167. A complete active prefix recovered 1200. A half-tail cut recovered a valid prefix of 1187, while a 20-byte log header failed startup. Missing required history or snapshot objects left the database unavailable although a server process survived. Six seconds of idle time did not advance the closed-only point. Twelve captures during writes, rotation, and snapshot finalization recovered valid prefixes containing their recorded source-before markers. This is qualified fixture evidence, not a fixed loss window or a zero-loss promise.

The control checkpoint is immutable within its managed generation and binds database, publisher owner, logical names, current program, canonical schema, configuration, and signing versions. Ordinary writes reused such a checkpoint. Controlled publication, names, configuration, ownership, or signing changes require their own fenced workflow and new checkpoint/epoch. Concurrent unmanaged control mutation is unsupported.

A deliberately mixed old control checkpoint and new data history recovered new data and module code but the old logical name. Data replay alone did not prove consistent whole-server metadata. A fresh checkpoint for the changed control generation then supported another online write and private replay of all 1202 rows with the expected empty name set and current module. Copying live Sled during compaction is not admitted. Routine archival reuses the sealed control checkpoint instead.

The earlier [online pilot](p09-online-pilot.md) also uploaded and independently downloaded four encrypted native captures through restic and scoped R2 storage. This new local gate does not claim another remote R2 restore. Protected encryption-key recovery and current Brine operation receipts belong to P09-05 and the physical drill.

## Durability and uncertain shutdown

Reducer acknowledgement is not durable evidence. One of three immediate ACK-only arm64 SIGINT trials exited 0 but lost row 1201. All three trials with an explicit confirmed read retained it. SIGTERM, SIGKILL, forced-stop timeout, and actual cgroup OOM stayed unknown even when confirmed rows replayed. The 16 MiB OOM case exited 137 with the actual OOM flag. The 64 MiB case hit the finite budget and was deliberately killed, not reported as OOM.

Three fresh publish/confirmed-read SIGINT runs and three of twelve successful race verifiers produced a cancelled-task panic at `crates/runtime/src/lib.rs:259` and exit 1. A settle delay did not fix this in the independent observer probe. No delay or exit-1-as-clean workaround is shipped. Unknown stop never proves an exact prepared point. Ordinary crash recovery can use fresh current-state inspection under retained fences when process/jobs/lifetime absence is independently proven.

An active WS fixture SIGINT exited 0 and recovered four confirmed rows. Scheduled reducers wrote three rows with clients disconnected. The engine remains a writer independently of the browser/app. Application and engine permits, restart jobs, lifetime holders, and fences therefore remain separate checks. The helpers here grant neither permit.

## Qualified metadata and canonical schema

`internal/spacetimedb.ObserveCurrent` uses fixed private GET requests for info, names, environment, schema-v10, and environment again. It requires matching current module hashes and expected database/publisher/name identity. Credentials and raw HTTP headers stay private. Redirects, proxies, oversized metadata, unknown fields, invalid identities, source drift, and expired/cancelled deadlines refuse with scrubbed errors.

The current program comes from owner-only environment metadata. Immutable `initial_program` stayed unchanged after an actual module update. Live SQL and WS may invoke client hooks. A physical counter fixture observed SQL lifecycle counts 1, 3, and 5, while five environment-schema-environment reads caused no extra increment. The fixed metadata path is the hook-free observer.

Raw schema JSON changes declaration order across restart. Production canonicalizer v2 expands `Ref`, exported `ty`, and table `product_type_ref` before sorting declaration sets. It sorts reducers as a set and preserves their positional parameter lists. Columns, enum variants, index columns, and default-value bytes remain ordered. It qualifies the seven fixture sections, acyclic type graphs, BTree indexes, and the observed native sequence shape. Unknown sections, recursive graphs, duplicate names, malformed or duplicate JSON, invalid references, and byte/type/node/depth/declaration overflow refuse.

The prototype `spacetimedb-v10-expanded-v1` omitted reducer-set sorting. Its historical hash `8cf5da7fa944310fa06ad8af84bf1cf42480ed1052424546d6ba59c917c2ab0c` remains a labelled pilot result. The Go v2 contract hashes the retained actual payloads to `f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45`. There is no v1 fallback.

The schema marker derives from canonicalizer version and schema SHA-256. It does not trust an application's marker assertion. The fixture's confirmed sequence/marker/time invariant remains independent data evidence.

## Initialized history and production startup boundary

Native healthy metadata cannot distinguish a fresh allocation from lost historical replicas when external bootstrap program bytes remain available. Initialized private replay withholds `program-bytes` from accessible mounts and requires recovery of current persisted `st_module`, expected names, owner, database, program, and schema. Fresh empty allocation and reviewed first publication are separate typed states.

The repository experiment seals a stopped source, copies only its bounded owned fixture tree, withholds bootstrap bytes in the copy, and checks source preservation and verifier disposal. Bubblewrap makes the original and external network inaccessible. Native startup may run init, lifecycle, or scheduled work only in this disposable copy. Its metadata result is not an exact captured data point.

Production `StoppedOwnedTree` and opaque `StartupProof` construction are P09-04 work. Existing host/store adapters must establish ownership, absence of processes/restart jobs/lifetime holders, committed binding/epoch, durable fences, and startup serialization through source-seal rechecks, private verification, disposal, and live launch. A location parser or arbitrary directory does not establish those facts. No managed startup path is enabled in this PR.

The separate exact-state native reader additionally found that a nonzero partial final commit header can be labelled `Complete` by the upstream decoder. Full-source offset, physical framing, native checksums, snapshot connectivity, and table multiset witnesses must therefore qualify exact capture independently. Metadata match and native `Complete` alone are insufficient. That reader's production distribution and capture integration are not shipped here.

## Program identity correction

Earlier research incorrectly called the native program hash BLAKE3. The pinned [hash implementation](https://github.com/clockworklabs/SpacetimeDB/blob/6b6e0ab19e4f8f52472aa4379552e9593c8f53bb/crates/sats/src/hash.rs#L53) uses Keccak-256. [Program construction](https://github.com/clockworklabs/SpacetimeDB/blob/6b6e0ab19e4f8f52472aa4379552e9593c8f53bb/crates/datastore/src/traits.rs#L522) and [program storage](https://github.com/clockworklabs/SpacetimeDB/blob/6b6e0ab19e4f8f52472aa4379552e9593c8f53bb/crates/core/src/host/disk_storage.rs#L53) use that hash.

For the earlier pinned Rust fixture, raw digest display was `72f02400f44e2127f7a256f3a3154089d3618a4dd75a184d7e5712dcf8404a59`. The environment API wire identity was `594a40f8dc12577e4d185ad74d8a61d3894015a3f356a2f727214ef40024f072`. They are exact byte reversals because Hash JSON uses little-endian u256. The observer preserves that exact wire identity without `0x`. Wasm SHA-256, native digest display, and native wire identity are distinct.

## Publication and user authentication

Private `pre_publish` with explicit `style=NoColor` is deterministic. The token is a nonexpiring UX hash of database and old/new program identities, with no secret or data-state binding. Actual tests rejected stale source-module, changed artifact, and different-database tokens. Ordinary confirmed row writes did not change it. Brine's immutable reviewed plan independently binds authority, expiry, exact source point, artifact, control generation, and canonical schema.

Native `AutoMigrate` accepted nonempty table removal and classified public-to-private access change as non-client-breaking. Neither is routine authorization. Destructive and access changes require independent typed diff policy. Defaulted-column backfill and marker preservation passed both the earlier labelled TypeScript schema/reducer change and the new matched Rust default-column fixture client. No delete-data or unrestricted yes flag ran.

The actual loopback Caddy JWT gate allowed only selected subscribe with an ordinary user token and websocket-token exchange with Bearer. Missing, forged, and correctly signed expired credentials returned 401; valid user credentials produced WS 101 and exchange 200. Reserved unknown/management paths denied before frontend fallback. No publisher token reached that proxy or browser. Anonymous fixture-public-read is a separate policy. External OIDC was not executed.

## Checks and remaining work

Default CI syntax-checks the optional gate and verifies its pins without network or host access. Go tests cover real payload equality, declaration/ref permutation, ordered-list changes, malformed/unsupported graphs, observer drift, fixed routes, private credential formatting/serialization, and budgets.

P09-04 through P09-08 still own managed spec/permit/startup wiring, exact-state reader qualification, authenticated remote capture/download, protected signing recovery, reviewed publication and source swaps, interruption/reboot recovery, and the physical Brine/browser/R2 drill. No simulated or earlier pilot result substitutes for those gates. The known cancellation panic is recorded in the owner's upstream report list; nothing was posted upstream.
