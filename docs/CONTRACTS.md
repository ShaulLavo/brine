# Brine v1 contracts

**Status: Approved design target, not an implemented API.** See [DECISIONS.md](DECISIONS.md) for where state lives, transport, Caddy, secrets and ports. The initial CLI still only exposes `version`, `doctor`, and the placeholder `tui`. Stabilize these contracts with tests before claiming compatibility.

## Command surface

~~~text
brine init [directory]
brine validate ./brine.toml
brine plan ./brine.toml --target staging
brine apply PLAN_ID --target staging
brine status --target staging
brine status --operation OPERATION_ID --target staging
brine logs APP --target staging --tail 100
brine rollback APP --release RELEASE_ID --target staging
brine backup status APP --target staging
brine restore test APP --target staging
brine tui --target staging
~~~

Plan is read-only with respect to runtime/proxy/data; it stores the plan in the target's control database (D1). `plan --offline` compares to a supplied snapshot and yields a **non-applyable** preview. Applying always refers to a recorded immutable plan and revalidates target identity, policy, observed generation, and artifact hashes. Rollback creates a **plan**, not an implicit mutation. Avoid an automatic `destroy` command.

## Machine modes

`--json` emits exactly one newline-terminated JSON object on stdout for each invocation, including command and flag errors. Stdout must be writable for this guarantee. Human help requested with `--json` is returned as a string in `data.help`. Without `--json`, commands retain human output. `--json=false` disables JSON, and flags after `--` are positional arguments.

`__complete` and `__completeNoDesc` use Cobra's shell-completion protocol. Their arguments describe another command line, so `--json` inside that command line does not change the protocol response.

`--no-input` is available. Terminal detection and prompt rules are tracked by P00-03. `--jsonl` is reserved for a later event stream and is not implemented. JSON and JSONL cannot be combined when JSONL becomes available. Machine output never includes ANSI codes, spinners, or prompts. Diagnostics go to stderr and never include raw subprocess output or argument values.

The response envelope is version 1. All five fields are always present:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema_version` | integer | Always `1` for this contract. |
| `command` | string | Canonical command path, such as `brine version`. Unknown commands use `brine`, never the unrecognized argument. |
| `ok` | boolean | `true` for success or an accepted operation, `false` for failure. |
| `data` | command-specific JSON value or null | Success payload. Always `null` on failure. |
| `error` | object or null | Always `null` on success. On failure, contains exactly `code`, `message`, and `retryable`. |

Error `code` is a stable string from the table below. `message` is a fixed safe string selected by that code, not a formatted underlying error. `retryable` is a boolean. A retryable conflict requires fresh state and a new plan or a released lock. It does not authorize blindly retrying an irreversible operation with an unknown outcome.

Example version response:

~~~json
{"schema_version":1,"command":"brine version","ok":true,"data":{"version":"0.1.1-dev"},"error":null}
~~~

Example usage failure:

~~~json
{"schema_version":1,"command":"brine","ok":false,"data":null,"error":{"code":"invalid_usage","message":"Invalid command or arguments. Use --help for usage.","retryable":false}}
~~~

A response that accepts a background operation says **accepted**, not **deployed**. Ctrl-C disconnects the observer, not the server-side operation.

### Local client doctor

`brine doctor` checks the **local client machine only**. It never connects to a target, changes files, installs software, or checks remote host readiness. Host inventory and readiness belong to P06-02. Per D1, Podman, systemd, Caddy, Litestream, and Tailscale are not local client requirements.

The local client requires `ssh`, the system OpenSSH executable used for transport. `git` is optional for working with app source repositories. Missing or failed optional tools do not fail doctor. A missing required tool or a failed required version probe returns `dependency_missing` and exit 3. This identifies a missing or unverified dependency, not a minimum supported version policy.

A successful response has `data.scope = "local_client"`, `data.remote_host_readiness = false`, `data.platform` with the client OS, and an ordered `data.checks` array. The envelope carries `schema_version`; the payload does not repeat it. Paths and raw subprocess output are omitted.

Each check always includes these fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `name` | string | `ssh` or `git`, in that order. |
| `required` | boolean | `true` for `ssh`; `false` for optional `git`. |
| `available` | boolean | Whether PATH lookup found an executable, independent of probe success. |
| `version` | string | Parsed version, or an empty string if the probe failed. |
| `reason` | string | Empty on success; otherwise `not_found`, `timeout`, `unparseable`, or `exit_error`. |

Version probes use separate executable and argument values: `ssh -V` and `git --version`. Each has a two-second timeout and captures at most 4096 combined stdout and stderr bytes. Only a recognized version token is rendered. OpenSSH writes its version to stderr; stderr is captured rather than passed through.

On Unix, each probe starts in its own process group. Cancellation or deadline expiry kills the group, including descendants that remain in it. Pipe draining remains bounded to 100 milliseconds. Non-Unix platforms retain immediate-process cancellation; descendant termination is not guaranteed there. Process groups are cleanup, not a sandbox: a program can deliberately leave its group.

Probes deliberately inherit the client environment, including tool and loader settings. Doctor trusts the executables selected by the client PATH; it does not isolate them from client credentials. Raw output stays private, but this runner is not an execution boundary for untrusted programs. A stronger use requires explicit environment selection and platform-specific process containment.

Human output identifies the local scope and shows each tool's requirement and version or failure reason. Failure JSON follows the shared envelope contract and carries `data: null`, `command: "brine doctor"`, and the fixed safe dependency error. It therefore contains no local report or per-tool detail. Partial results on failure, explicit local-scope fields on failures, and a safe message naming the missing tool require a later versioned contract extension.

## Error codes and exit categories

| Code | Exit | Retryable | Safe message |
| --- | --- | --- | --- |
| `internal_error` | 1 | false | The operation failed. |
| `invalid_usage` | 2 | false | Invalid command or arguments. Use --help for usage. |
| `dependency_missing` | 3 | false | A required dependency is missing or incompatible. |
| `policy_refused` | 4 | false | The operation was refused by policy. |
| `conflict` | 5 | true | The plan is stale or another operation holds the lock. |
| `recovery_required` | 6 | false | Manual recovery is required before continuing. |
| `interrupted` | 130 | false | The client was interrupted. |
| `tui_interactive` | 2 | false | tui is interactive; remove --json and --no-input |

| Exit | Category |
| --- | --- |
| 0 | Success or accepted operation. |
| 1 | Internal or operational failure, including unclassified errors and stdout write failures. |
| 2 | Validation or usage error, including unknown commands, unknown flags, unexpected arguments, and TUI machine-flag refusal. |
| 3 | Incompatible or missing required dependency. |
| 4 | Refused by policy. |
| 5 | Stale plan or lock conflict. |
| 6 | Manual recovery required. |
| 130 | Interrupted client, including context cancellation and SIGINT. |

The domain error model lives in `internal/result` and has no Cobra or Charm dependency. Error causes remain available for internal inspection but are never rendered. Wrapped context cancellation takes priority over other categories. `internal/cli.Execute` owns the complete invocation response, including Cobra failures. `NewRootCommand` constructs a command tree for embedded use but does not own final error presentation. `main` maps the returned error to the exit status.

## App definition

Strict versioned `brine.toml` for one trusted OCI web app. Unknown fields fail, app names/domains are canonicalized and validated, environment keys are checked, and image references must contain a digest. Disallow free-form Quadlet snippets, arbitrary shell hooks, host path mounts, raw proxy JSON, arbitrary Podman flags, and target endpoint injection.

**Illustrative, not runnable** until the placeholder digest is replaced with a real digest from a fixture image:

~~~toml
schema_version = 1
name = "hello"
image = "ghcr.io/example/hello@sha256:<64-hex-digest>"
container_port = 3000
domains = ["hello.example.com"]

[health]
path = "/healthz"
expected_status = 200
startup_deadline_seconds = 30
timeout_seconds = 3

[resources]
memory_mb = 256
pids_limit = 128

[environment]
APP_ENV = "production"

[secrets]
SESSION_KEY = "hello-session-key"  # Podman secret name, never a value
~~~

Secrets are **references** to Podman secrets owned by the runner user on the target (D5); never render secret values into plan output or git. Target policy controls allowed registries, domains, ports, secret IDs, persistence roots and public exposure. Resource settings are enforced by the adapter rather than blindly passed through.

## Plan and durable state

A plan binds desired spec hash, target identity, observed generation, image digest and platform, policy version, secret-reference versions, artifact digests, expiry and change summary. Identical canonical inputs should generate identical hashes; timestamps live outside hash material. Plan acceptance is not a general authorization grant.

### Pure planner contract, schema 1

`internal/plan.Build(Input)` accepts `policy.Desired`, `target.Snapshot`, caller-supplied pinned image metadata and `plan.Evidence`. It performs no I/O and reads no clock. The returned plan owns its memory. Treat it as immutable after hashing. Expiry, creation time, approval, transport provenance and offline/not-applyable status belong in a caller-owned envelope, never inside the hashed plan. Offline provenance must remain explicitly not applyable when P01-04 and P01-05 add their presentations and files.

The snapshot schema describes Caddy file hashes and used port numbers, but cannot identify domain or live listener ownership. It also cannot reconstruct the previous environment, health and resource settings. `Evidence` supplies observed routes, listener owners and committed release fingerprints with artifact hashes. Obtain these facts from the same target and generation as the snapshot. Known empty arrays are affirmative observations, not defaults. Unknown or unsupported required facts produce a conflict with no changes. An empty route or listener app owner means unrelated software. A used existing app port without affirmative listener ownership also conflicts. No domain ownership is guessed from a Caddy filename.

The image metadata must name the exact digest pinned in the desired image and its resolved OS and architecture. Missing metadata or a different digest is an input error, not permission to resolve an image online. A platform mismatch produces a conflict. Secrets bind to the highest positive numeric version with the exact name `brine-<app>-<ref>-v<n>` in that app's inventory. Bindings include the environment name, logical reference, immutable Podman name and Podman ID. No secret values are accepted or resolved. Precreated secrets can live in an app inventory record with an absent current release.

Port allocation keeps an existing app port, even when a later policy range changes. Otherwise it selects the lowest policy-range port absent from listeners, used ports and all app reservations. No-op requires a matching committed configuration fingerprint, the expected image, the app's container unit and Caddy file, and unchanged recorded artifact hashes. A missing release fingerprint produces an update, not a guessed no-op. Recorded artifact drift conflicts.

Changes have typed payloads in this order: allocate a port if needed, pull and verify the image, bind existing secret versions in environment-name order, render Quadlet, stage the complete next Caddy generation, restart the app. The Caddy payload preserves every unrelated file and replaces only the owned app file. Staging is intent, not publication. Later apply phases own whole-config validation, quiescing, health checks, activation, publication and rollback. A conflict has no changes.

Fingerprints use lowercase `sha256:<hex>` over compact Go `encoding/json` output. There are no maps in hash material. Set fields sort on defensive copies. Desired domains sort lexically; desired environment and secret entries sort by environment name. Snapshot apps, units, secrets and Caddy files sort by name; used ports sort numerically. Evidence routes sort by domain then app, listeners by port then app, applied records by app and their units by name. Diagnostics sort by code then field and duplicates collapse. Changes retain execution order.

- `DesiredHash` hashes exactly `policy.Desired.CanonicalBytes()`, without a trailing newline. This includes the normalized app, policy version, policy hash and port range.
- `ConfigHash` hashes the JSON object with fields in order `desired`, `image`, `port`, `secrets`. `desired` is the canonical desired JSON object, not an encoded string. The other fields contain the expected image, selected host port and ordered immutable secret bindings. `Applied.ConfigHash` records this fingerprint only after a release commits; it must not be synthesized from the new desired input.
- `Hash` hashes the JSON object with fields in order `desired`, `snapshot`, `evidence`, `plan`. The first three are canonical JSON objects. `snapshot` is the complete `target.Encode` result with its terminal newline removed by JSON compaction. This conservatively includes all observed facts, including disk and dependency observations, rather than trying to predict which future apply checks need them. `plan` is the complete output, including schema, kind, app, target identity and host-key fingerprint, observed generation/status, policy version/hash, desired/config hashes, expected image/platform, host port, secret bindings, ordered changes and diagnostics, with its `hash` field set to the empty string. The final output replaces that empty field with the computed fingerprint. Canonical plan serialization adds one terminal newline, which is not hash material.

The target identity, observed generation and policy version/hash are explicit preconditions for a later apply under the host lock. P01-03 records and fingerprints these preconditions; it does not implement freshness checking or authorize mutation.

Use a small SQLite control database **on the target**, owned by the runner user, for plans, releases, operations and append-only ordered events (D1). Clients keep only target configuration. It is **separate** from app SQLite databases and needs its own recovery story. Use a bounded connection pool, transactions around local state only, and migrations. Choose a maintained SQLite Go driver in a deliberate dependency PR.

A deployment writes intent and operation state durably *before* changing files, Quadlet units, containers or proxy routes. Each tool boundary has a typed adapter with bounded outputs and timeouts. Acquire a per-host lock **before** checking plan freshness; one mutation per target. Reconcile observed state after crashes: neither subprocess exit status nor client timeout alone proves an external change succeeded or failed.

## Release behavior

For MVP accept brief interruption: pull/verify a new image, stage immutable release/unit material, announce maintenance, ensure old writer stopped, activate new release, start service, check direct and routed health, publish route, record success. Maintain previous known-good immutable artifacts. If safe, restore previous app release on failure; if migrations or data compatibility make rollback uncertain, stop and mark `recovery_required`.

Use state labels such as `queued`, `preflight`, `preparing`, `quiescing`, `starting`, `checking`, `committing`, `rolling_back`, `succeeded`, `failed`, `rolled_back`, and `recovery_required`. Persist ordered events with monotonically increasing per-operation sequence IDs. Do not perform network or host operations inside a long SQLite database transaction.

**App rollback never rewinds SQLite.** v1 does not perform uncontrolled startup migrations or auto-restore databases. Background/worker overlap is not supported yet.

## Runtime ownership and authorization

Enroll each host with affirmative operator action and a read-only inventory. Refuse conflicts with existing software, ports, domains and volumes; never uninstall other software because a plan mentions a proxy (D4). Use a dedicated rootless Podman service user and systemd user units. Test Quadlet install/reboot semantics on the pinned distribution, not merely against a local mock.

Caddy is the host's packaged service. Brine owns only its generation directories under `/etc/caddy/brine/`, validates the complete candidate config before each change, reloads through a polkit-scoped `systemctl reload caddy.service`, serializes changes, detects drift by hash, and preserves all unrelated site configuration (D4). Apps never reach the admin API.

Rootless does **not** mean safe for an untrusted agent. A deployment identity that can SSH freely and control Podman can bypass Brine checks and can alter its own app data. Autonomous production mode requires an operator-owned policy and the restricted dispatcher (`brine host serve` as a forced SSH command, D1), whose permissions exclude root, host deletion, raw Podman/Caddy control and cloud teardown.

## Database and recovery

SQLite data, WAL and related files live in durable per-app directories independent of release images. Run **one independent Litestream replicator per database/destination** (matching a tested Litestream release), with separately scoped R2 credentials and destination paths. Replication is asynchronous and may lose recent writes when the VPS dies. Do not start competing replicas writing the same destination.

`restore test` writes into a new isolated directory or disposable fixture, checks `PRAGMA integrity_check`, verifies expected application invariants and reports recovery-point information if available. It **never overwrites the production DB**. Live restore stays an explicitly approved operator runbook. Database storage or backup deletion is never part of image cleanup.

## Source references

The upstream manuals for [Quadlet](https://docs.podman.io/en/stable/markdown/podman-systemd.unit.5.html), [Caddy admin API](https://caddyserver.com/docs/api), [Litestream R2](https://litestream.io/guides/s3-compatible/) and [Litestream caveats](https://litestream.io/tips/) are useful starting points; implementation must pin and test actual installed versions.
