# Reviewed first-database initialization

Status: Approved

Deploy and application startup preserve schema. Creating a new persistent app therefore requires a separate operation before its first compatible release starts:

1. Prepare persistent data with `brine data prepare brine.toml --target NAME`, then apply that preparation plan. Preparation registers the declared schema and existing database/replica identities, allocates private directories, and records absence evidence. It does not create SQLite files.
2. Deliver backup credentials through `brine backup credentials set`.
3. Have the operator review and install the schema initializer below.
4. Plan and apply initialization. Initialization uploads and independently restores an empty snapshot itself. Operators do not fabricate restore receipts.
5. Make a fresh deployment plan, then deploy the first release accepting the initialized marker with `startup = "preserve"`.

The initialization reference named `first_release_plan` can be the retained data-preparation plan: its frozen app declaration identifies the intended first release without incorrectly requiring that release to accept an empty database. Initialization never applies that release plan or starts app code.

## Review boundary

`SchemaDefinition` specifies the database, marker and normative application catalog SHA-256. It is not executable SQL. A separately reviewed initializer contains that exact definition and bounded schema-creation statements:

```json
{
  "definition": {
    "database": "main",
    "marker": "v1",
    "catalog_sha256": "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"
  },
  "statements": ["CREATE TABLE t(x TEXT)"]
}
```

The operator reviews the exact file bytes, computes their SHA-256, and installs the file as `/etc/ssh/brine/data-init/schemas/<64-hex-digest>.json`. The file and its ancestors must be root-owned, not group/other writable, and symlink-free. A typical operator installation is:

```sh
artifact=$(sha256sum reviewed-schema.json | cut -d ' ' -f 1)
sudo install -d -o root -g root -m 0755 /etc/ssh/brine/data-init/schemas
sudo install -o root -g root -m 0644 reviewed-schema.json "/etc/ssh/brine/data-init/schemas/$artifact.json"
printf 'sha256:%s\n' "$artifact"
```

This is an explicit operator action; enrollment and agent requests cannot install artifacts. The definition must exactly match both the registered definition and the intended release declaration. The artifact is compiled in an isolated in-memory database and its resulting catalog hash checked before live-file creation. Supported statements are single `CREATE TABLE`, `CREATE INDEX`, `CREATE UNIQUE INDEX` and `CREATE VIEW` statements, without semicolons. Trigger bodies and arbitrary migration/data statements are outside this minimal operation.

The request supplies only app, retained release/preparation plan ID and artifact digest. SQL, arbitrary filesystem paths, approval booleans, operator identity and caller-installed restore receipts are rejected.

## Operator policy

Restricted agents cannot initialize data unless policy explicitly enables `allow_agent_migrations = true`. The default is false. The local operator command `brine host data-init APP` uses local operator composition and cannot be selected through restricted dispatch.

Explicit freshness and recovery bounds are also required, including for the operator:

```toml
allow_agent_migrations = false

[data_initialization]
max_backup_age_seconds = 300
max_restore_test_age_seconds = 60
recovery_window_seconds = 86400
```

Zero/missing bounds refuse initialization. Maximums are one day for upload age, five minutes for restore-test age, and thirty days for the recovery window. Current nonexpiring retention evidence and exact binding/epoch credential evidence are required. No caller-supplied bounds override operator policy.

## Commands

```sh
brine data init APP --target NAME --plan \
  --first-release-plan sha256:PREPARATION_PLAN_DIGEST \
  --artifact sha256:REVIEWED_ARTIFACT_DIGEST
brine data init APP --target NAME --plan-id sha256:INITIALIZATION_PLAN_DIGEST
```

Local operator execution uses the same flags with `brine host data-init APP`, without `--target`. Apply does not accept planning-only inputs. Initialization plans bind the requester, policy, target identity/generation, desired declaration, exact database binding, replica epoch/prefix, source state, destination definition and generated restore-point ID.

## Empty restore point and durable boundaries

The minimal operation admits exactly one never-started database and an uncommitted replica. It refuses a committed release, writer history, unresolved writer-start intent, active operation, foreign fence, observed app unit, unknown inventory, nonempty/unknown schema, or missing allocation evidence for an absent file. It consumes preparation's bindings and receipt; it does not reserve identities or create live data directories.

Under the host mutation lock, initialization claims a durable database fence and one-attempt journal record atomically. Both application and replica startup permits honor the fence. Fresh inventory and never-started identity checks establish that no registered writer exists; this path does not stop or initialize a previously running app.

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

Apply of an already-attempted plan only inspects. It never uploads, invokes the initializer, or creates another attempt. A freshly verified matching destination plus the durable independently verified empty point can be adopted as success. A still-empty, zero-byte, missing, changed or unknown destination requires operator recovery and keeps its fence. Timeouts are not permission to retry irreversible work.

Inspection after success is not a migration and does not start code. A new deployment plan is needed because initialization changes schema facts. Database rollback remains distinct from application rollback.

## Evidence and limits

Unit tests cover initializer refusal, atomic schema/marker creation, reference-only requests, default-off authority, explicit bounds, conditional-upload capability/collision/unknown failures, independent empty restore, and interruption at each journal boundary. These tests use private local fixtures and fake HTTP storage, not a production restore claim.

The physical acceptance drill is: prepare a fresh disposable app, deliver limited backup credentials, initialize from an operator-reviewed artifact, verify the remote empty point with an independent restore, deploy a schema-preserving first release, write a sentinel, restart it, and prove the same rows and marker survive. Repeat interrupted initialization boundaries and prove neither the app nor replica starts while its fence remains held. No Pi or live R2 action is performed by this implementation lane; the existing P04 acceptance checkbox remains open until that drill supplies evidence.
