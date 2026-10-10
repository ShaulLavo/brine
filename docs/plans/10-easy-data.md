# Phase 10: Easy app data

**Status: Approved** (owner, 2026-10-10). Execute P10-01 to P10-03 before P09-04, so SpacetimeDB uses the same contract from the start. No task is complete because this plan exists.

## Why

The Pi drills proved Brine's data guarantees, but adding a database to an app takes far more ceremony than Kamal (a volume plus an environment variable) or Coolify (click, paste a connection URL). Today an app author must:

- keep `SQLITE_PATH` in the app and `mount_path`/`filename` in `brine.toml` in sync by hand;
- copy a catalog SHA-256 into `[[schema_definitions]]`;
- hand-write an initializer JSON, hash it, and `sudo install` it under `/etc/ssh/brine/data-init/schemas/` on the server;
- run prepare, credentials, init and deploy as four separate commands.

Goal: **declare the database in `brine.toml`, keep the schema in the app repo, deploy.** Keep every guarantee from D11/D12: no schema change on startup, reviewed initialization, verified empty restore point, operator-only data authority by default, fail-closed fences.

## Non-goals

- **No SDK or driver wrapper.** Apps use any SQLite build, driver and language. Kamal and Coolify ship none either; the environment is the API. Docs may show short examples, not packages.
- No ORM or migration framework. Brine runs reviewed DDL; apps own their queries.
- No compatibility paths for the current manual flow; it is replaced (no users yet).

## The database file contract (documented in P10-05)

Brine needs only:

1. The file lives where Brine mounts it, and the app opens the path Brine provides.
2. WAL journal mode, left on. The app does not run checkpoints that fight Litestream (`wal_autocheckpoint` default is fine; no `TRUNCATE` checkpoints of its own).
3. The `brine_schema_marker` table is created by Brine and never dropped or edited by the app. Reading it is optional and recommended.
4. The standard SQLite file format. Custom builds, compile options, newer versions and function-only extensions are fine. File-format changes (encryption such as SQLCipher, nonstandard page formats) are refused at admission because restore tests open backups with Brine's own SQLite. Custom collations or virtual tables used by the schema need a declared checker extension, which is out of scope until requested.

## Tasks

- [ ] **P10-01 Environment contract.** Brine sets, per declared database, read-only variables in the app container: `BRINE_DB_<NAME>_ENGINE` (`sqlite`), `BRINE_DB_<NAME>_PATH` (container path, e.g. `/data/app.db`) and `BRINE_DB_<NAME>_ACCEPTS` (comma-separated accepted markers). `<NAME>` is the database name upper-cased with `-` mapped to `_`. Values derive from the normalized spec, are part of the desired hash, and appear in plans. A user `[environment]` key starting with `BRINE_` is refused at validation. SpacetimeDB adds `_URL` and `_NAME` in P09-04 using the same rule. **Acceptance:** golden env rendering for one and several databases; name mapping collisions refused; `BRINE_` user keys refused; changing a mount path changes the plan. **Evidence:** spec/plan/quadlet unit tests; Pi fixture reads `BRINE_DB_MAIN_PATH` instead of `SQLITE_PATH` (update `fixtures/sqlite-app`).

- [ ] **P10-02 Schema files in the app repo.** `[[databases]]` gains `schema = "db/main.sql"`, a path relative to `brine.toml`, read by the client only. The file holds the same bounded DDL subset accepted today (`CREATE TABLE`, `CREATE INDEX`, `CREATE UNIQUE INDEX`, `CREATE VIEW`), one statement per `;`-terminated entry. The client compiles it in an in-memory SQLite with the same compiler and catalog observer the host uses, derives the marker (default `sha256:<first 12 hex of catalog>`, or an explicit `marker =`), the catalog SHA-256 and the initializer artifact. `[[schema_definitions]]` and `catalog_sha256` disappear from hand-written specs; `[[schema_compatibility]]` defaults to accepting the current file's marker. The host recompiles the artifact and rejects any mismatch. **Acceptance:** identical catalogs on client and host for every supported statement kind; unsupported statements, multiple statements per entry, triggers, and data statements refuse client-side with the line number; whitespace/comment-only edits do not change the marker; a real schema change does. **Evidence:** golden catalog hashes shared by client and host tests.

- [ ] **P10-03 Reviewed initialization without sudo.** Replace the manual `sudo install` step. The init plan carries the artifact bytes and shows the SQL and resulting catalog for review. Applying it with operator authority makes the host store the artifact itself in the existing root-owned location (written by the privileged dispatcher, `0644`, digest-named, symlink-free) before initialization. Restricted agent credentials still cannot initialize unless `allow_agent_migrations = true`; the plan states which authority it needs. **Acceptance:** an agent-scoped apply refuses before storing anything; tampered artifact bytes refuse; a second apply of the same digest is idempotent; interruption between store and init reconciles without a duplicate attempt. **Evidence:** dispatcher authority tests; interruption cases next to T23.

- [ ] **P10-04 One command for a new app.** `brine deploy brine.toml` on an app whose databases are not yet initialized prints one combined review: data preparation, credential status, schema initialization (with SQL), then the release. On approval it runs the existing operations in order, each with its own journal and detached task, stops at the first failure, and resumes from the first unfinished step when rerun. Backup credentials are never minted: if none are delivered, it asks for `--backup-credentials FILE` (same 0600 file format as `backup credentials set`) or stops with the exact command to run. **Acceptance:** happy path end to end; failure at each step leaves earlier steps intact and a rerun continues; an already initialized app deploys as today; `--json` output lists each step's operation ID. **Evidence:** CLI orchestration tests with fake host; the Pi drill in P10-06.

- [ ] **P10-05 Docs: "Using a database".** One page: the contract above, a minimal `brine.toml`, a schema file, and 10-line examples for TypeScript (`bun:sqlite` and `better-sqlite3`), Rust (`rusqlite`) and Go (`modernc` or `mattn/go-sqlite3`), each opening `BRINE_DB_MAIN_PATH`, enabling WAL and busy timeout, and checking the marker. Include how to add a column later (points to the reviewed migration flow) and what custom SQLite builds may and may not do. Replace the manual steps in `docs/data-initialization.md`.

- [ ] **P10-06 Pi drill.** Under D3: create a new app from a repo schema file with one `brine deploy`; verify env variables in the container, initialization, replication and a restore test. Repeat with a fixture built against a different SQLite version or compile options to show custom builds pass. Interrupt P10-04 at each step and rerun to completion.

## Order

P10-01 can start now. P10-02 then P10-03. P10-04 after both. P10-05 alongside P10-04. P10-06 last. P09-04 (SpacetimeDB planning/ingress) waits for P10-01 so both engines share the environment contract.
