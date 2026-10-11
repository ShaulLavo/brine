# Reviewed first-database initialization

Status: Approved

Deploy and application startup preserve schema. For a new app, the default
workflow composes reviewed initialization before its first compatible release:

```sh
# Existing externally issued credentials; Brine does not mint keys.
chmod 600 backup-credentials.json
brine deploy brine.toml --target NAME --backup-credentials backup-credentials.json
```

The combined review shows data preparation, current credential status, each
schema's exact SQL and resulting catalog, then the intended release. Type `yes`
to approve, or use `--yes` after review. `--json` without `--yes` returns only the
review; `--no-input` never prompts. Delivered credentials need no file argument.
A missing packet stops before any apply and prints the exact command to run.

The operations run in order with separate detached jobs and journals. A failed
step stops later work. Rerun the same command, pinned target and unchanged spec
to observe the unfinished job and continue, without repeating successful steps.
The private client resume journal contains only safe IDs and completion flags;
no credential values or app source. Keep that journal when recovering a lost
response. `--json` lists each step's operation ID, also on failure.

Unknown or recovery-required initialization still requires inspection and
reconciliation. A rerun does not replay a possibly executed schema mutation.
After a settled failure and affirmative unfenced observations, a rerun may
create a fresh reviewed plan. Already initialized apps use ordinary release
planning/apply; startup never changes schema.

The individual `data prepare`, `backup credentials`, `data init`, `plan` and
`apply` commands remain available for examining each boundary separately.
Initialization itself uploads and independently restores an empty snapshot;
operators do not fabricate restore receipts. Each database receives its own
credential and initialization operations before the release starts.

The initialization reference named `first_release_plan` can be the retained data-preparation plan: its frozen app declaration identifies the intended first release without incorrectly requiring that release to accept an empty database. Initialization never applies that release plan or starts app code.

## Review boundary

An app declares a client-side schema file under its database:

```toml
[[databases]]
name = "main"
persistent_root = "/srv/brine-data"
mount_path = "/data"
filename = "app.db"
backup_destination = "primary"
schema = "db/main.sql"
# Optional. Omit this to derive sha256:<first 12 catalog hex digits>.
marker = "v1"
```

The path is clean and relative to `brine.toml`. Only the client reads it, using
a directory-confined open; symlink files and directory components are refused.
The file is bounded to 1 MiB and 128 `;`-terminated entries. Each entry is one
`CREATE TABLE`, `CREATE INDEX`, `CREATE UNIQUE INDEX` or `CREATE VIEW` statement.
Triggers, table creation from query results and migration/data statements are
refused with their source line. Brine removes SQL comments and normalizes
inter-token whitespace before compiling in an in-memory SQLite database.
Whitespace/comment-only edits normally do not change the catalog marker;
literal and identifier contents remain significant. SQLite uses expression
source text to name unaliased view columns. If normalization changes those
names, Brine retains the original view statement rather than changing its
schema. Give view expressions explicit `AS` aliases (or a view column list) to
keep formatting edits independent of their column names and marker. An empty
file is refused.

The client uses the host's compiler and catalog observer to derive the catalog
SHA-256, marker and initializer bytes. Handwritten `[[schema_definitions]]` and
`catalog_sha256` are no longer spec fields. Without an explicit
`[[schema_compatibility]]` entry, that database accepts only its current file's
marker with `startup = "preserve"`. An explicit compatibility entry remains
unchanged. It may accept older schemas but never authorizes a migration.

Export the exact initializer JSON for review:

```sh
brine data schema brine.toml --database main > reviewed-schema.json
```

Without a machine flag, export writes canonical JSON bytes with no added newline. With `--json` or `--jsonl`, the normal result envelope contains `data.artifact` metadata and a `data.bytes` string holding those exact bytes. Its SHA-256 matches
the initializer digest shown in deployment and data-preparation plans, including
machine output. Deployment and preparation transport carry only catalog/marker/digest metadata. The separate initialization plan carries the reviewed bytes and catalog. Deployment still changes no schema.

The client reads a bounded regular initializer file (at most 1 MiB), hashes its exact bytes and sends those bytes with the digest in the initialization planning request. The host independently recompiles the SQL in isolated in-memory SQLite and rejects a catalog mismatch. Human plans show the SQL and resulting catalog; machine plans include `artifact_bytes` (JSON base64), the exact digest and `catalog`. The immutable plan binds all of them. At apply, the host stores the approved artifact by digest in its own private control database, only after claiming the durable fence and one-attempt intent. Planning retains the review payload but does not publish an apply artifact. There is no sudo step, root-owned schema directory or privileged writer.

The request supplies only app, retained release/preparation plan ID, artifact digest and artifact bytes. Host filesystem paths, approval booleans, requester identity and caller-installed restore receipts are rejected. Apply supplies only app and the retained initialization plan ID.

## Authority and policy

The owner and their agent are one personal deployment identity (D8/D12, owner decision 2026-10-11). Anyone who can apply reviewed plans over the restricted channel can initialize data. There is no separate agent migration permission or local-only initialization command. Protected policy, exact review, independent host compilation, empty-source checks and restore verification still apply.

Explicit freshness and recovery bounds are required:

```toml
[data_initialization]
max_backup_age_seconds = 300
max_restore_test_age_seconds = 60
recovery_window_seconds = 86400
```

Zero/missing bounds refuse initialization. Maximums are one day for upload age, five minutes for restore-test age, and thirty days for the recovery window. Current nonexpiring retention evidence and exact binding/epoch credential evidence are required. No caller-supplied bounds override protected policy.

## Commands

```sh
brine data init APP --target NAME --plan \
  --first-release-plan sha256:PREPARATION_PLAN_DIGEST \
  --artifact reviewed-schema.json
brine data init APP --target NAME --plan-id sha256:INITIALIZATION_PLAN_DIGEST
```

Apply records and launches a detached host job, returning an accepted operation ID promptly. The CLI polls status to a terminal result by default; `--no-wait` returns that ID without polling. Each transport call keeps its short request deadline; losing the observer never cancels accepted host work. Inspect it with `brine status --operation OPERATION_ID --target NAME`. Reapplying the same plan recovers the same accepted job and never launches another initialization.

Apply does not accept planning-only inputs. Initialization plans bind the requester, policy, target identity/generation, desired declaration, exact database binding, replica epoch/prefix, source state, destination definition and generated restore-point ID.

## Empty restore point and durable boundaries

The minimal operation admits exactly one never-started database and an uncommitted replica. It refuses a committed release, writer history, unresolved writer-start intent, active operation, foreign fence, observed app unit, unknown inventory, nonempty/unknown schema, or missing allocation evidence for an absent file. It consumes preparation's bindings and receipt; it does not reserve identities or create live data directories.

Under the host mutation lock, initialization claims a durable database fence and one-attempt journal record atomically, then stores the digest-bound reviewed artifact. An interruption between storage and initialization remains an attempted operation and is inspected, never blindly restarted. Both application and replica startup permits honor the fence. Fresh inventory and never-started identity checks establish that no registered writer exists; this path does not stop or initialize a previously running app.

While fenced, Brine:

- Creates a valid empty staging SQLite database outside live data.
- Uploads it to `<epoch-prefix>/restore-points/<point-id>/snapshot.sqlite` using signed `If-None-Match: *`. The newly generated point ID is reserved in the immutable plan. A second conditional PUT of the same harmless empty bytes must return 412, proving create-only capability. A colliding point, unsupported condition or unknown network outcome refuses the operation. There is no HEAD-then-PUT race and no upload retry after an unknown result.
- Independently downloads the exact object into isolation and verifies declared size/SHA-256, integrity, foreign keys and empty catalog.
- Records the verified point before schema-mutation intent.
- Rechecks identities, policy, source emptiness and restore-point freshness.
- Atomically creates the reviewed schema and exact `brine_schema_marker` in one SQLite transaction.
- Observes the destination afresh, records success and consumes untouched allocation evidence before releasing the fence.

The journal boundaries are intent, quiesced, restore-point intent, verified restore point, mutation intent, completed mutation and success. Each unknown intermediate result retains the fence. Live files are never deleted during compensation. Remote snapshot objects are never deleted by this operation.

## Unknown outcomes

The initialization engine’s recovery path only inspects an already-attempted plan. Repeated CLI apply returns the same immutable detached job outcome; it does not rerun or automatically reconcile that job. It never uploads, invokes the initializer, or creates another attempt. A freshly verified matching destination plus the durable independently verified empty point can be adopted as success. A still-empty, zero-byte, missing, changed or unknown destination requires operator recovery and keeps its fence. Timeouts are not permission to retry irreversible work.

For an interrupted job, the supported local recovery command is `brine host reconcile`, run as the enrolled Brine account. Boot reconciliation uses that same path. It first settles abandoned runners, then acquires the host mutation lock and independently proves the initialization runner is dead and has no pending systemd job. Unknown/running manager observations keep the fence held. Reconciliation retains the recorded typed requester; job attribution remains bound to the original restricted-channel identity.

Inspect-only recovery adopts a freshly verified destination only when its exact marker/catalog and durable independently verified empty restore point match. If an allocation receipt proves the file was never created, or fresh catalog inspection proves it is still empty, recovery records terminal `not_initialized` and atomically releases the fence without consuming allocation absence evidence. No upload, schema statement, file deletion or live repair is replayed. A zero-byte, changed, nonempty or unknown file keeps the fence held. The original detached outcome remains immutable; the reconciliation report and initialization journal record the settlement. To initialize after `not_initialized`, review and create a new initialization plan; the old attempt cannot be reused.

Inspection after success is not a migration and does not start code. A new deployment plan is needed because initialization changes schema facts. Database rollback remains distinct from application rollback.

## Evidence and limits

Unit tests cover initializer refusal, atomic schema/marker creation, reviewed-byte planning and reference-only apply requests, single-identity authority, digest idempotence, tamper refusal, explicit bounds, conditional-upload capability/collision/unknown failures, independent empty restore, and interruption at each journal boundary. These tests use private local fixtures and fake HTTP storage, not a production restore claim.

The physical acceptance drill is: prepare a fresh disposable app, deliver limited backup credentials, initialize from an operator-reviewed artifact, verify the remote empty point with an independent restore, deploy a schema-preserving first release, write a sentinel, restart it, and prove the same rows and marker survive. Repeat interrupted initialization boundaries and prove neither the app nor replica starts while its fence remains held. No Pi or live R2 action is performed by this implementation lane; the existing P04 acceptance checkbox remains open until that drill supplies evidence.
