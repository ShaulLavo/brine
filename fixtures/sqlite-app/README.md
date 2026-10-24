# SQLite physical-drill fixture

This deliberately small, unauthenticated HTTP app is for disposable private
SQLite backup drills, not production or public exposure. It shares Brine's module
and fixed schema observer so marker/catalog rules do not drift. Its row shape
matches the restore checker's `fixture_commits` sentinel.

## Image and runtime contract

The **Publish SQLite drill fixture** workflow publishes
`ghcr.io/shaullavo/brine-sqlite-fixture` for `linux/amd64` and `linux/arm64`.
Run it after this change lands, or use its main-branch push run. Copy the exact
`ghcr.io/shaullavo/brine-sqlite-fixture@sha256:...` reference from its job summary
into the app spec. Tags are discovery aids only; never deploy `latest`.
The first publish must succeed before a digest-pinned drill is possible.

The runtime uses `scratch` (an empty filesystem, with no registry base image
or floating tag) and one CGO-disabled Go binary. No compiler, shell, or
package manager ships. It runs as **UID/GID 10001:10001**; declare those numeric
IDs in Brine's persistent runtime so its keep-id mapping owns DB/WAL/SHM files.
Brine mounts the database declaration's directory and injects the database
connection settings. Declare database name `main`, mount path `/data` and
filename `app.db`. The app requires **`BRINE_DB_MAIN_PATH`** and opens exactly
that path, with no default or legacy variable fallback. Brine also supplies
`BRINE_DB_MAIN_ENGINE=sqlite` and `BRINE_DB_MAIN_ACCEPTS=fixture-v1` from the
spec. These names cannot be overridden by app environment settings or secrets.
Parent directories are never
created by the app. Brine supplies private filesystem modes and umask.

HTTP listens on **8080**. Configure Brine's health path as `/health`.
SQLite connections use WAL mode and `busy_timeout=5000` milliseconds.
Startup fails on an absent, empty, unmarked, or incompatible database. Every
write verifies the expected marker and actual catalog inside the same
transaction as its insert. Startup and health never create or migrate schema.

## Schema file and reviewed initialization

`brine.toml` declares `schema = "schema.sql"` and `marker = "fixture-v1"`
under the `main` database. Replace its placeholder image digest and domain
before a private drill. Brine reads the SQL relative to that TOML file on the
client, derives the catalog hash and initializer, and defaults compatibility
to `accepts = ["fixture-v1"]` with `startup = "preserve"`. The fixture keeps an
explicit marker because its app code checks that marker. Other apps may omit
`marker` to use `sha256:<first 12 catalog hex digits>`.

Generate the exact artifact bytes for operator review:

```sh
brine data schema fixtures/sqlite-app/brine.toml --database main > reviewed-schema.json
sha256sum reviewed-schema.json
```

The catalog SHA-256 is
`f7a92d3e7a27c89c3afd455307ec78b2417b6e643aefbcf1c7c33c3c59838341`.
The artifact-file digest is different and appears in connected and offline
plans. Follow [the reviewed initialization flow](../../docs/data-initialization.md).
Use `--artifact reviewed-schema.json` when making the initialization plan.
The client sends the exact bytes; the human plan shows SQL and the resulting
catalog. Apply stores them by digest in the unprivileged host's private state.
No manual server installation or sudo step is required.

`data init` creates the fixed `brine_schema_marker` table and row atomically
with the reviewed statements. Do not add that table to the SQL file or execute
schema.sql at app startup. Prepare data and establish the required independently
verified empty backup point before initialization. The app bypasses none of
those gates. Schema-file behavior is verified locally; the updated physical
drill remains unverified.

## HTTP drill contract

| Request | Result |
| --- | --- |
| `GET /health` | 200 `{"ok":true}` only for compatible schema; otherwise 503 |
| `POST /rows`, JSON `{"marker":"sentinel-1"}` | 201 object with `sequence`, `marker`, `committed_at` |
| `GET /rows/count` | 200 `{"count":N}` |
| `GET /rows/latest` | 200 latest row with the same three fields, or 404 when empty |

POST markers must be 1–1024 bytes. Requests are bounded to 4096 bytes. Sequence
is the SQLite integer primary key (also the row ID). `committed_at` is the UTC
RFC3339Nano timestamp captured just before the insert; responses are sent only
after commit succeeds. It is a fixture timestamp, not a measured durable-commit
clock. Keep the returned fields as the restore sentinel (`table` is
`fixture_commits`). The server supplies no raw SQL endpoint.

Physical acceptance: initialize via Brine, deploy the **manifest digest** on
rootless arm64, record a POST response, restart/replace/reboot, and verify count,
latest sequence, marker, and timestamp survive. Independently restore the
recorded remote point and verify that sentinel. Include simultaneous Litestream
replication to prove ownership and short-lock tolerance on the actual host.
Local unit tests do not establish those hardware/storage claims.
