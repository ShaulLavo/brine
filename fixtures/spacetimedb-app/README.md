# SpacetimeDB recovery fixture

This small fixture tests module identity, authenticated writes, browser subscriptions and recovery on disposable private instances. It contains a Rust/Wasm module, the official client SDK and a static Go asset server. It is not a game framework. The app never creates a database or publishes a module on startup.

## Pinned build and publication

The module uses Rust **1.96.1**, exact SpacetimeDB SDK **2.11.0** and retained Cargo.lock. The published crate requires Rust 1.96.0; the upstream tagged toolchain pins 1.96.1. Client SDK **2.11.0**, TypeScript **5.6.3**, Bun **1.4.2** and Playwright **1.64.0** have retained lockfiles. Official CLI/runtime **2.11.0** comes from a release archive checked against SHA-256 `fe4645f3abc96a5386ebe98879bba0bea2d0bf56ad7612c322e36a7b93ba04d0`.

`build.sh` builds each module into its own target directory and checks a second independent v1 build for identical bytes. Rust remaps crate/toolchain/module source prefixes so panic metadata cannot embed host-specific cache paths. It generates the official client bindings directly from each compatible Wasm, checks their types, bundles browser assets and cross-compiles the asset server for amd64 and arm64. `module/artifact-pins.json` records the exact module and client artifact identities. `manifest.py` produces a publication manifest binding those bytes to the source commit and input hashes.

Set `BRINE_FIXTURE_BUILD_ROOT` to a new private scratch directory and keep local toolchains/caches on your data drive. Install the pinned toolchain and run:

```sh
rustup toolchain install 1.96.1 --profile minimal --target wasm32-unknown-unknown
./fixtures/spacetimedb-app/build.sh
bun install --cwd fixtures/spacetimedb-app/browser-test --frozen-lockfile
fixtures/spacetimedb-app/browser-test/node_modules/.bin/playwright install chromium
python3 fixtures/spacetimedb-app/smoke.py "$BRINE_FIXTURE_BUILD_ROOT"
```

The **Publish SpacetimeDB drill fixture** workflow runs build and native/browser checks on pull requests. Only a successful main-branch verification job can publish to `ghcr.io/shaullavo/brine-spacetimedb-fixture`. It retains module/client hashes, the source revision and actual image index/platform digests. Use the immutable index from that workflow's summary, never a tag. Publication and both architectures' execution evidence remain required before P09-03 is complete.

The serving image is `scratch` with one static binary, about 6 MiB per architecture, and UID/GID **10001:10001**. Image assembly sets the entrypoint mode to 0555 even when artifact transfer reduces files to 0644. The workflow removes executable bits, builds both images and checks actual entrypoint startup plus HTTP/browser identity before publication. Arm64 runs under CI emulation; native Pi execution remains a physical gate. It contains no compiler, shell, module publisher, signing key or backup credential. It embeds the same v1 browser assets for both architectures. SpacetimeDB runs in a separate pinned engine service.

## App environment and HTTP contract

The fixture consumes alias `main` through these exact variables. No alternative names substitute for them.

| Variable | Required value |
| --- | --- |
| `BRINE_DB_MAIN_ENGINE` | `spacetimedb` |
| `BRINE_DB_MAIN_URL` | HTTP or HTTPS SDK base ending in `/`, without userinfo, query or fragment |
| `BRINE_DB_MAIN_NAME` | The logical upstream database name |
| `BRINE_DB_MAIN_ACCEPTS` | `brine-recovery-v1` |

The managed public base can be `https://fixture.example/_brine/db/main/`. SDK 2.11.0 appends `v1/database/<NAME>/subscribe` and `v1/identity/websocket-token` relative to that base. Ingress allows the selected database's client routes and denies management routes. Publisher access uses a separate private endpoint.

HTTP listens on **8080**. `BRINE_FIXTURE_LISTEN` is an optional bind address for private local tests. `GET /health` checks the asset service, not database or backup readiness. `GET /config.json` exposes only `endpoint` and `database`; no secret or owner token is copied into it. The browser keeps its user token in memory and clears the input after connecting. It uses no browser storage. Module Wasm and source/client helper files are not served.

## Module and recovery markers

The public tables `recovery_left` and `recovery_right` store the same `sequence`, `marker`, `commit_time` and `scheduled` values in one transaction. Sequences are contiguous from one and capped at 10,000. Marker one is `brine-p09-00000001`. `commit_time` is the reducer-context transaction timestamp, not a measured fsync clock. A public immutable `schema_marker` row has ID one and revision `brine-recovery-v1`.

The initial publisher identity is stored in a private table. Only that identity can authorize up to four writer identities or schedule 1..20 writes at one-second intervals. Only authorized identities can append the next sequence. Client writes refuse while the scheduler runs; a schedule must fit the remaining row budget. The scheduled reducer accepts the database's own scheduler identity, not a client caller. Stopping the browser does not stop that writer.

`ConfirmedSession` explicitly sets `.withConfirmedReads(true)`. Writes await both the reducer promise and the corresponding confirmed subscription row. It checks paired tables, contiguous sequences, deterministic markers, timestamps and the fixed schema marker. A raw unconfirmed SDK connection cannot construct a recovery session. `unconfirmed-drill.ts` is a negative probe that emits only an acknowledgement with `durabilityEvidence:false`.

Application markers do not replace authoritative module/schema observation. The local smoke test calls Brine's shared `tools/spacetime-runtime` helper, using canonicalizer **spacetimedb-v10-expanded-v2**, and requires the source and independently restarted copy to have the same metadata observation. It does not contain another canonical schema renderer. Artifact SHA-256 differs from SpacetimeDB's native Keccak-256 program identity; the API's hash wire representation also differs from its native digest/display. The fixture uses the shared observer for that representation.

## Explicit private drill client

Use token files with private modes. The publisher helper is for disposable fixture preparation, not a deployment startup hook or a public browser route.

```sh
BRINE_PUBLISHER_TOKEN_FILE=/private/publisher.token bun fixtures/spacetimedb-app/client/admin-drill.ts ENDPOINT NAME authorize WRITER_IDENTITY_HEX
BRINE_PUBLISHER_TOKEN_FILE=/private/publisher.token bun fixtures/spacetimedb-app/client/admin-drill.ts ENDPOINT NAME schedule 3
BRINE_USER_TOKEN_FILE=/private/user.token bun fixtures/spacetimedb-app/client/drill.ts ENDPOINT NAME write 1
BRINE_USER_TOKEN_FILE=/private/user.token bun fixtures/spacetimedb-app/client/drill.ts ENDPOINT NAME read
BRINE_USER_TOKEN_FILE=/private/user.token bun fixtures/spacetimedb-app/client/drill.ts ENDPOINT NAME verify 1
BRINE_USER_TOKEN_FILE=/private/outsider.token bun fixtures/spacetimedb-app/client/drill.ts ENDPOINT NAME denied
```

A write supplies the expected next sequence. Inspect an unknown outcome before taking another action. The clients never retry a write automatically. Publisher authorization uses the official SDK's Identity type instead of guessed HTTP JSON arguments.

## Reviewed candidate artifacts

Rust features are mutually exclusive and are built as distinct Wasm artifacts. These are test inputs, not runtime version branches.

| Variant | Change and client pairing |
| --- | --- |
| `v1` | Four evidence columns and no lifecycle hook; the published app serves this client |
| `additive` | Adds public `fixture_note`; subscribed row layouts stay unchanged; both base and regenerated clients preserve markers |
| `default-column` | Appends `rating: u8` with default seven to both tables; existing column and default order stays intact; requires its regenerated client and browser assets |
| `destructive` | Removes the nonempty `recovery_right` table; independent Brine review must reject it before effects |
| `access-change` | Changes `recovery_left` from public to private; access changes require independent review |
| `observer-hooks` | Adds `client_connected` that changes the marker; the fixture's confirmed invariant reader refuses the changed marker |

Native preflight labels nonempty table removal **AutoMigrate** with `break_clients=true`, and public-to-private access change **AutoMigrate** with `break_clients=false`. Those flags do not establish safe data or access changes. The defaulted-column candidate uses its own freshly generated SDK; no parser accepts both row layouts. It is not compatible with the published v1 client image. Negative candidates are not serving or rollback releases.

`smoke.py` runs the actual SDK and headless browser against the pinned local runtime. It proves denied writes, explicit confirmed WebSocket events and browser writes, bounded scheduling without clients, stopped-tree marker replay through the shared observer, unconfirmed-acknowledgement refusal, additive preservation and regenerated default-column decoding. Private keys and tokens stay outside its retained evidence and are removed after the run.

Immediate SIGINT after some module updates can exit nonzero in the pinned runtime. The smoke test records those exits without an exact capture receipt. A clean exit alone also does not create a receipt. This local fixture test proves neither zero loss, production capacity nor remote S3 recovery; those are separate runtime and physical gates.
