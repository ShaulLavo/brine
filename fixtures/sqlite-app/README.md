# SQLite physical-drill fixture

This deliberately small, unauthenticated HTTP app is for disposable private
Phase 04 drills, not production or public exposure. It shares Brine's module
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
Brine mounts the database declaration's directory; there is no automatically
injected Brine database-path environment variable. Declare mount path `/data`
and filename `app.db`. The app reads **`SQLITE_PATH`**, default `/data/app.db`;
set it explicitly if changing the declaration. Parent directories are never
created by the app. Brine supplies private filesystem modes and umask.

HTTP listens on **8080**. Configure Brine's health path as `/health`.
SQLite connections use WAL mode and `busy_timeout=5000` milliseconds.
Startup fails on an absent, empty, unmarked, or incompatible database. Every
write verifies the expected marker and actual catalog inside the same
transaction as its insert. Startup and health never create or migrate schema.

## Reviewed initialization artifacts

- `fixtures/sqlite-app/schema.sql`: reviewed application DDL, no marker DDL.
- `fixtures/sqlite-app/schema.json`: the exact reviewed initializer format
  accepted by `brine data init` (PR #77). Database name `main`, marker
  `fixture-v1`, catalog SHA-256
  `f7a92d3e7a27c89c3afd455307ec78b2417b6e643aefbcf1c7c33c3c59838341`.

Use database name `main` and register the initializer's `definition` in the
operator schema definitions. Declare release compatibility for `main` with
`accepts = ["fixture-v1"]` and `startup = "preserve"`. `data init` creates the
fixed `brine_schema_marker` table and row atomically with the reviewed statements;
do not add that table to the initializer or execute schema.sql at app startup.

Follow the data-init operator review/install flow: hash the **exact JSON file
bytes**, install that artifact under its hash in the protected operator schema
location, and pass that artifact digest to the planned `data init` operation.
The artifact-file hash is different from the application catalog hash above.
Prepare data and establish the required independently verified empty backup
point before initialization. The app does not bypass any of those gates.

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
