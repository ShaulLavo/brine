# Using a database

Declare a SQLite database, keep its schema in your app repository, and run
`brine deploy`. Brine prepares the storage, initializes the reviewed schema and
releases your app. Your app uses an ordinary SQLite driver: no Brine SDK,
connection service or migration-on-startup hook is needed.

First initialization and one-command deployment are implemented. Existing-schema
migration execution is not yet implemented (P04-07); the last section explains
that boundary. This page is not a production-readiness or verified-backup claim.

## The file contract

For a database named `main`, Brine injects these container environment variables:

| Variable | Value in the example below |
| --- | --- |
| `BRINE_DB_MAIN_ENGINE` | `sqlite` |
| `BRINE_DB_MAIN_PATH` | `/data/app.db` |
| `BRINE_DB_MAIN_ACCEPTS` | `notes-v1` |

Open the supplied path, not a host path or a hardcoded replacement. The path is
the declared container mount path joined with the filename. For other database
names, upper-case the name and replace `-` with `_`: `audit-log` becomes
`BRINE_DB_AUDIT_LOG_PATH`. Accepted markers are sorted and comma-separated.

Brine owns these inputs. Do not set `BRINE_` keys in `[environment]` or `[secrets]`;
validation refuses them. They are not an OS restriction on changing a process's
own environment. These examples are for `_ENGINE=sqlite` only.

Your app must:

- Open the existing file read/write and keep **WAL mode** enabled. Keep the
  database, `-wal` and `-shm` files on the mounted storage; do not move or delete
  them. Give each connection a busy timeout (the examples use five seconds).
- Leave checkpoints to SQLite and Litestream. SQLite's default
  `wal_autocheckpoint` is fine; do not run your own `TRUNCATE` checkpoints or a
  competing checkpoint loop.
- Preserve the schema on startup: no `CREATE TABLE IF NOT EXISTS`, `ALTER TABLE`,
  ORM auto-migrations or marker updates. Ordinary inserts, updates and deletes
  in your application's tables are fine.
- Never drop or edit `brine_schema_marker`. Brine creates it with columns
  `id`, `marker` and `catalog_sha256`; its single row has `id = 1`. Reading its
  marker is optional and recommended. Checking it in the app is an extra guard,
  not a substitute for Brine's independent catalog and startup checks.
- Use the standard SQLite file format so Brine's independent restore checker
  can open the same data.

## Declare the database and schema

Keep these two files in the app repository:

```text
brine.toml
db/main.sql
```

A minimal `brine.toml`:

```toml
schema_version = 1
name = "notes"
# Placeholder: replace with your actual published, digest-pinned app image.
image = "ghcr.io/example/notes@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
container_port = 8080
domains = ["notes.example.test"]

[runtime]
uid = 10001
gid = 10001

[[databases]]
name = "main"
persistent_root = "/srv/brine-data"
mount_path = "/data"
filename = "app.db"
backup_destination = "primary"
schema = "db/main.sql"
marker = "notes-v1"
```

Replace the image and domain with real, policy-approved values. The image must
serve HTTP on port 8080 and satisfy the configured health check (the default
path is `/health`). The enrolled target's protected operator policy must already
allow the app, runtime UID/GID and persistent root, define the `primary` backup
destination and initialization freshness bounds, and admit its declared backup
retention. This app file neither creates a storage bucket nor changes host policy.

`db/main.sql`:

```sql
CREATE TABLE notes (
    id INTEGER PRIMARY KEY,
    body TEXT NOT NULL
);
CREATE INDEX notes_body ON notes(body);
```

Do **not** include `brine_schema_marker`; Brine adds it itself. Omit `marker` if
you prefer the derived `sha256:<first 12 catalog hex digits>` marker. Without an
explicit `[[schema_compatibility]]`, Brine accepts only the current file's marker
and uses `startup = "preserve"`.

The schema file is relative to `brine.toml`, read on the client, and confined to
that directory tree: symlink files/components are refused. It is limited to
1 MiB and 128 semicolon-terminated entries. Each entry is one `CREATE TABLE`,
`CREATE INDEX`, `CREATE UNIQUE INDEX` or `CREATE VIEW`; triggers, virtual tables,
`CREATE TABLE ... AS SELECT`, data statements and migration statements are not
accepted. Unsupported input fails on the client with its source line.

The client derives the catalog fingerprint and exact initializer bytes. The
host independently compiles and verifies them. Do not handwrite catalog hashes,
`[[schema_definitions]]` or initializer JSON. Whitespace/comment-only edits
normally preserve the marker; alias view expressions explicitly to keep their
column names stable. See [initialization internals](data-initialization.md#review-boundary)
for the precise normalization rules.

## Deploy with one command

Obtain scoped backup credentials externally for the operator-defined destination.
Brine never mints keys. Put the existing packet in a private regular file, using
the same JSON format as `backup credentials set`:

```json
{"access_key_id":"EXTERNALLY_ISSUED_ACCESS_KEY","secret_access_key":"EXTERNALLY_ISSUED_SECRET"}
```

Include `session_token` and RFC 3339 `expires_at` when the issuer supplies them;
do not invent or extend credential lifetime. Those are placeholders, not
credentials. Keep the real packet out of your
repository, image, logs and app environment.

```sh
chmod 600 backup-credentials.json
brine deploy brine.toml --target NAME --backup-credentials backup-credentials.json
```

The command reviews storage preparation, credential status, exact initialization
SQL and its resulting catalog, then the release. Type `yes` to approve. It runs
separate detached operations in order, stopping at the first failure; initialization
includes an independently verified empty restore point before any schema mutation.
No sudo, remote schema copy or manually fabricated restore receipt is needed.

If credentials are already delivered, omit `--backup-credentials`. Missing
credentials stop before apply and print the exact command to run. To review from
an agent, use `--json` without `--yes`; after review, `--yes` authorizes execution
and JSON lists the accepted operation IDs, including on failure. Machine mode
never prompts. `--no-input` without approval changes nothing.

Rerun the same command with the unchanged spec and pinned target to observe
accepted work and continue unfinished steps. Preserve the private resume journal
under your target configuration directory. Do not blindly retry an uncertain
initialization: inspect the reported operation with
`brine status --operation OPERATION_ID --target NAME` and follow
[unknown-outcome recovery](data-initialization.md#unknown-outcomes).

Checkpoint references are host-validated, not approval by themselves. If release
decisions change after approval, Brine shows the new diff and asks again in an
interactive release; `--yes` and machine modes stop with `conflict` and require a
fresh review. An already initialized app uses ordinary release planning; deploying
a new image never applies a new schema.

## Open it from your app

These are connection-opening examples, not complete HTTP servers. Pin the driver
version in your app's own lockfile. They refuse an absent file, missing marker or
unsupported marker instead of creating or repairing a database. Keep the connection
for your app's normal queries; close it on shutdown. Configure busy timeout on
every connection in a pool, not just one startup connection.

### TypeScript: Bun's `bun:sqlite`

```ts
import { Database } from "bun:sqlite";
const path = process.env.BRINE_DB_MAIN_PATH;
if (!path) throw new Error("BRINE_DB_MAIN_PATH is required");
const db = new Database(path, { readwrite: true, create: false });
db.exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;");
const row = db.query("SELECT marker FROM brine_schema_marker WHERE id = 1").get() as { marker: string } | null;
const accepts = (process.env.BRINE_DB_MAIN_ACCEPTS ?? "").split(",");
if (!row || !accepts.includes(row.marker)) throw new Error("Unsupported database schema");
// Query notes here; call db.close() when the app shuts down.
```

### TypeScript: `better-sqlite3`

```ts
import Database from "better-sqlite3";
const path = process.env.BRINE_DB_MAIN_PATH;
if (!path) throw new Error("BRINE_DB_MAIN_PATH is required");
const db = new Database(path, { fileMustExist: true });
db.pragma("journal_mode = WAL");
db.pragma("busy_timeout = 5000");
const row = db.prepare("SELECT marker FROM brine_schema_marker WHERE id = 1").get() as { marker: string } | undefined;
const accepts = (process.env.BRINE_DB_MAIN_ACCEPTS ?? "").split(",");
if (!row || !accepts.includes(row.marker)) throw new Error("Unsupported database schema");
// Query notes here; call db.close() when the app shuts down.
```

### Rust: `rusqlite`

```rust
use rusqlite::{Connection, OpenFlags};
use std::{env, time::Duration};
fn main() -> Result<(), Box<dyn std::error::Error>> {
    let db = Connection::open_with_flags(env::var("BRINE_DB_MAIN_PATH")?, OpenFlags::SQLITE_OPEN_READ_WRITE)?;
    db.pragma_update(None, "journal_mode", "WAL")?;
    db.busy_timeout(Duration::from_secs(5))?;
    let marker: String = db.query_row("SELECT marker FROM brine_schema_marker WHERE id = 1", [], |row| row.get(0))?;
    let accepts = env::var("BRINE_DB_MAIN_ACCEPTS")?;
    if !accepts.split(',').any(|accepted| accepted == marker) { return Err("Unsupported database schema".into()); }
    Ok(()) // A real server keeps db alive until shutdown.
}
```

### Go: `modernc.org/sqlite`

`mode=rw` forbids creation. The driver's `_pragma` connection option applies the
busy timeout to every connection, including those reopened by `database/sql`.

```go
package main

import (
    "database/sql"
    "net/url"
    "os"
    "slices"
    "strings"
    _ "modernc.org/sqlite"
)

func main() {
    path := os.Getenv("BRINE_DB_MAIN_PATH")
    if path == "" { panic("BRINE_DB_MAIN_PATH is required") }
    u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_pragma=busy_timeout%285000%29"}
    db, err := sql.Open("sqlite", u.String())
    if err != nil { panic(err) }
    defer db.Close()
    db.SetMaxOpenConns(1)
    var mode, marker string
    if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil { panic(err) }
    if mode != "wal" { panic("WAL is required") }
    if err = db.QueryRow("SELECT marker FROM brine_schema_marker WHERE id = 1").Scan(&marker); err != nil { panic(err) }
    if !slices.Contains(strings.Split(os.Getenv("BRINE_DB_MAIN_ACCEPTS"), ","), marker) { panic("Unsupported database schema") }
    // Query notes here; a real server keeps db alive until shutdown.
}
```

## Custom SQLite builds

You can use another SQLite version, compile options or function-only extensions
as long as files and schema stay readable by Brine's SQLite compiler and restore
checker. No exact app-driver version match is required. The declarations and
backup/restore proofs still have to pass independently; a custom build is not
permission to bypass them.

SQLCipher/encrypted files and nonstandard page/file formats are unsupported:
Brine's own SQLite must be able to open the backup and verify integrity, foreign
keys and catalog. Do not encrypt or rewrite the file format from app code.
Schema-dependent custom collations, virtual tables and extension-dependent schema
expressions need checker support too; no declared checker-extension mechanism
is shipped. Do not put those in the schema or assume loading an extension in
the app makes it available to Brine. App-only functions used in ordinary queries
are fine if they leave the stored schema and standard file format unchanged.

## Add a column later

Changing the schema file and deploying **does not migrate an existing database**.
`ALTER TABLE` is not allowed in the initialization file, and a new marker is not
proof that live data has changed. Do not execute DDL or edit the marker on startup,
and do not reuse `data init` against an existing database.

The intended [reviewed migration flow](CONTRACTS.md#schema-migrations) is separate
from deployment. For example, adding `title TEXT` to `notes` would require a
reviewed `ALTER TABLE notes ADD COLUMN title TEXT` migration with a freshly
verified pre-migration restore point, writer/replica quiescence, fenced atomic
DDL plus the new marker/catalog, and explicit compatibility for application
rollback. Update the repository's full `CREATE TABLE` schema to describe the
resulting catalog; keep migration SQL separate from that initialization file.

**That executor is not yet shipped.** Follow
[P04-07's implementation gate](plans/04-sqlite-and-backups.md#p04-07-policy-delivery-and-implementation-gate)
before planning a live schema change; there is no supported migration command to
copy from this page. Keep the current schema until that reviewed flow is available,
or arrange a separate explicitly authorized operator procedure with the same
backup, fencing and verification guarantees. Never substitute a startup migration.

A future compatible release may declare old and new markers explicitly:

```toml
[[schema_compatibility]]
database = "main"
accepts = ["notes-v1", "notes-v2"]
startup = "preserve"
```

Declare this only if the code genuinely works with both schemas. It lets the
release open either already-verified schema; it runs no SQL, creates no missing
column and does not make an old incompatible image safe to roll back. Database
rollback and application rollback remain separate operations.
