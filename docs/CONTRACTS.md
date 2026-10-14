# Brine v1 contracts

**Status: Approved design target; implementation is tracked per task.** See [DECISIONS.md](DECISIONS.md) for where state lives, transport, Caddy, secrets and ports, and the phase plans for shipped behavior and remaining acceptance gates. Connected planning and stateless deployment/recovery are implemented; `tui` remains a welcome screen. Do not infer that every contract below is shipped.

## App-owned UDP and WebTransport (D10, Phase 08; implementation pending)

The [UDP-only delivery](plans/08-udp.md) owns policy-approved UDP allocation,
publishing, inventory and lifecycle recovery. The app owns its game protocol,
authentication and any app-managed TLS. Plain UDP requires no certificate,
WebTransport declaration or echo-health interface. Its endpoint facts contain
the approved client address, port, protocol and exposure, not a guessed
WebTransport URL. Implementation remains pending.

Optional TLS file delivery is P08-11 in [the extension plan](plans/08-webtransport.md).
`file_secrets` binds immutable app-scoped certificate/key versions.
App-managed hashes leave short-lived certificate rotation and trusted browser
`serverCertificateHashes` delivery to the app, with no Brine key injection or
claim of normal public trust. WebTransport browser/draft interoperability is
application evidence, separate from UDP deployment readiness.

Default readiness combines existing HTTP health with ownership of the exact expected UDP mapping/listener. Protocol-session verification and remote UDP reachability are separate facts and remain unverified unless actually probed. Optional WebTransport health requires an explicit bounded echo contract or a supported app-specific protocol; ordinary-health Rust servers do not implement it merely to deploy. P08-05 specifies the optional nonce exchange and refusal budgets. A successful HTTP/3 GET never substitutes for that opted-in protocol check. Shared UDP 443 and private previews remain separately gated.

## App database environment (P10-01)

Apps need no Brine SDK. They use any standard-format SQLite build or driver
and open the container path Brine supplies. Each declared database receives:

| Variable | Value |
| --- | --- |
| `BRINE_DB_<NAME>_ENGINE` | `sqlite` |
| `BRINE_DB_<NAME>_PATH` | The declared mount path joined with the filename, such as `/data/app.db` |
| `BRINE_DB_<NAME>_ACCEPTS` | Accepted schema markers, sorted and comma-separated |

`<NAME>` is the database name upper-cased with `-` mapped to `_`. Database
names remain lower-case letters, digits and hyphens under the existing grammar;
duplicate names and mapped-name collisions are refused. The spec reserves every
`BRINE_` name in both `[environment]` and `[secrets]`, even without databases.
App environment files are not supported and the generated app unit never reads
one. The image's defaults are superseded by explicit container environment
settings. Read-only means Brine-owned deployment input, not an OS restriction
on a process changing its own environment.

The values derive from normalized database declarations and compatibility sets.
Both sources and generated values are canonical desired-hash material; changing the name, mount
path, filename or accepted markers changes the desired hash. Generated values
appear in the plan's `database_environment` field and in JSON and human plan
presentations, including conflict and no-op deployment plans. Ordinary user
environment values remain redacted. Quadlet renders the generated variables as
explicit, escaped `Environment` assignments. Missing schema compatibility still
refuses deployment at admission; these settings do not initialize or migrate a
database and do not weaken D11/D12 startup checks.

Local spec, policy, plan, presentation and Quadlet tests establish this contract.
The SQLite fixture now requires `BRINE_DB_MAIN_PATH`. Container environment and
SQLite behavior on physical hardware remain unverified here; P10-06 owns that
Pi drill.

## Command surface

### Managed SpacetimeDB deployment and lifecycle (P09-04)

The app declaration accepts one `[[databases]]` entry with
`engine = "spacetimedb"`, the existing alias/root/backup-destination fields and
a nested `[databases.spacetimedb]` service declaration. The payload binds a
digest-pinned image, pinned runtime version, logical database name, module Wasm
and schema-v10 sidecar paths, runtime identity, bounded resources and distinct
publisher/signing/auth references. The database entry also requires a distinct
`recovery_password_ref` for encrypted native backup recovery. Shared `[[schema_compatibility]]` selects
accepted markers for the alias with `startup = "preserve"`. The current marker
is the default accepted marker. Mixed SQLite and SpacetimeDB declarations are
refused in this first delivery. Existing SQLite canonical records are unchanged.

Application `image` and `container_port` remain a pair. Both may be omitted for
an engine-only declaration. Such a declaration has no frontend port, container,
data mount or application secret input. When present, the app receives
`BRINE_DB_<NAME>_ENGINE=spacetimedb`, `_URL`, `_NAME` and sorted `_ACCEPTS`.
The URL uses the first sorted owned domain and fixed managed prefix
`/_brine/db/<alias>/`, including its trailing slash. It has no SQLite `_PATH`.
Browser assets obtain only public URL/name settings through their app's config.

Protected operator `[spacetimedb]` policy supplies license-use authority and
explicit capture/verification intervals, maintenance and remote-loss budgets.
The 24-hour/seven-day proposals are not automatic admission or a durability
guarantee. Combined engine and frontend resource requests fit operator ceilings.
An omitted frontend resource request uses the remaining memory and PID ceilings
after the engine request, and refuses when either remaining budget is empty.
Engine credentials cannot be reused as app secrets.

Connected managed planning separates fresh preparation from initialized service deployment.
Fresh preparation reserves the app incarnation, database, archive destination, and
loopback ports. Apply creates the owned private native directory and disabled
module-HTTP configuration. It records an unpublished database with no serving
release, native runtime, frontend permit, or client route. Preparation never
uploads Wasm or starts a database or frontend.

Initialized deployment requires the current protected native runtime, module,
schema, secret versions, owned unit, source observation, and unfenced serving
scope. The optional frontend uses an ordinary Quadlet with independent ownership.
An engine-only release has no frontend component. Frontend deployment and removal
preserve the current database, module, keys, and epoch. Application rollback
restores only compatible frontend artifacts and reviewed component intent.

Start, stop, and restart use the existing operation engine. With a frontend,
these actions affect the frontend and client route. For an engine-only app, they
affect the native service and client route. Durable desired and route revisions
record deliberate pause and resume independently of release history. Stopping
requires owned process and file evidence. Serving requires current live permits.
Every native start, frontend boot, and route publication checks current intent.

Initialized recovery inspects completed boundaries without repeating effects.
A committed deployment requires its immutable release plus fresh native, desired,
route, frontend unit, manager-job, container, image, port, and loaded-Caddy proof.
Completed lifecycle recovery requires its ordered component journal and current
intent and route receipts. An unknown or incomplete boundary remains recovery
required. Resolve retains the original receipt and uses a bounded same-plan
successor chain. A successful successor grants serving only when adopted steps,
current scope, and immutable artifact ownership agree before terminal success.

After a compatible native change, historical recovery proof grants no new scope.
A fresh ordinary lifecycle plan can authorize the existing compatible frontend
under the current runtime. Failed deployment compensation uses that exact prior
desired revision, completed restoration journal, and restored route and unit.
The failed attempt remains distinct from the prior serving authority.

Backup-only configuration changes return `managed_backup_revision_required` and
cannot silently restart components or activate a new backup policy through deploy.
Whole managed removal remains refused without the separate exact-native-archive
consumer. These local orchestration and filesystem checks do not establish a
working native browser connection, restart drill, or R2 restore.

Ingress permits selected-database WebSocket subscription and temporary
WebSocket-token exchange with credential presence. It denies other managed
requests before frontend fallback. The engine authenticates JWTs; Caddy does not
validate them. Native forwarding and boot/fence qualification require the
separate physical acceptance drill.

The executable route guard matches the original literal client path, so escaped
and cleaned path aliases cannot pass the allowlist. Subscription queries retain
SDK fields such as `confirmed`, `compression` and `connection_id`, but require
one nonempty token and reject duplicate token keys, including encoded aliases.
Malformed query encoding refuses. Both original decoded and normalized managed
namespaces terminate before frontend fallback, including traversal out of the
reserved prefix. Optional local checks drive the actual pinned Caddy config
against private HTTP stubs. They qualify route selection, not native JWT or
WebSocket subscription behavior.

Wasm artifact SHA-256 and native runtime identity are distinct. The pinned
runtime hashes program bytes with Keccak-256; API JSON encodes a little-endian
u256, unlike its display digest. Client `ParseFile` reads confined regular files
relative to the app declaration, refuses symlink components, and bounds Wasm to
64 MiB and schema-v10 JSON to 1 MiB. It uses the production v2 canonicalizer and
retains both program identities and the canonical schema hash as untrusted
desired input. The explicit app marker and shared accepted markers stay intact;
the sidecar creates no schema registration or initializer. Host dispatch never
opens the client paths. Reviewed private staging must prove actual module/schema and
register its marker before startup authority exists. A sidecar cannot grant it.

### Managed ingress logging admission (P09-04)

The pure `caddy.VerifyManagedLogging` helper reads complete adapted JSON before
managed route admission. It requires an explicit protected default logger and
checks every output receiving relevant error, enabled access and reverse-proxy
logs. A qualifying output uses the exact filter/JSON encoder with
`request>uri delete`, with only deletion filters, or an explicit `discard`
writer. Token-only query filtering refuses because malformed request targets
can bypass Caddy's query parser. Access logging being disabled does not protect
default error logs.

Host matches and logger mappings account for casing and wildcard matches;
default fallback namespaces remain checked even when access skip rules exist.
Unprotected shadow outputs, dynamic route logger names or `log_append` handlers
on matching routes, credential logging, handler tracing, unknown filters,
duplicate keys and ambiguous logging input refuse without exposing input values.
Unrelated logger namespaces remain
allowed. The helper performs no mutation and grants no managed effects.

The pinned Caddy 2.10.2 server has three route roots: primary `routes`, server
`errors.routes` and `named_routes`. For a server serving a managed domain, all
error and named routes are checked, along with every nested handler/error tree.
Only primary routes with a host matcher excluding all managed domains are
skipped. Unknown server fields, route-field aliases and malformed route objects
refuse. This includes error routes that inherit the original request without
their own host matcher.

The operator establishes protected global logging through enrollment or an
explicitly authorized change. D4 still forbids routine Brine operations from
editing the root Caddyfile after enrollment. See the
[operator logging prerequisite](managed-ingress-logging.md) for the pinned
snippet and local admission/log probe. Production caller wiring and physical
ingress qualification remain separate P09-04 gates.

~~~text
brine init [directory]
brine validate ./brine.toml --policy ./policy.toml
brine plan ./brine.toml --target staging
brine plan ./brine.toml --offline --snapshot ./snapshot.json --policy ./policy.toml [--state ./brine-state.json] [--out ./plans]
brine apply PLAN_ID --target staging
brine status [APP] --target staging
brine status --operation OPERATION_ID --target staging
brine reconcile --target staging [--dry-run]          # settle already-accepted interrupted operations
brine logs APP --target staging --tail 100
brine rollback APP [--release RELEASE_ID] --target staging
brine config set APP KEY=VALUE --target staging        # env, resources, domains, health
brine secret set APP NAME --target staging             # value from stdin; stores a new immutable version, unbound until applied
brine restart|stop|start APP --target staging
brine remove APP --target staging                      # stateless removal plan; data archival waits for P04-08 (D8)
brine data purge ARCHIVE_ID --target staging           # only if policy allows agent purge (D8)
brine restore live APP --backup BACKUP_ID --target staging   # only if policy allows (D8, P04-09)
brine diagnose [APP] --target staging                  # read-only report
brine host update --target staging                     # Brine-managed packages only (D8)
brine host restart-caddy --target staging              # affects every site on the host (D8)
brine host cleanup --target staging                    # Brine-owned leftovers only
brine host reboot --target staging
brine backup status APP --target staging
brine restore test APP --target staging
brine tui --target staging
~~~

Plan is read-only with respect to runtime/proxy/data; it stores the plan in the target's control database (D1). `plan --offline` compares to a supplied snapshot and yields a **non-applyable** preview. Applying always refers to a recorded immutable plan and revalidates target identity, policy, observed generation, and artifact hashes. Every mutating command above (`config set`, `restart`/`stop`/`start`, `rollback`, `remove`, `data purge`, `restore live` and the `host` verbs) returns a **plan**, and nothing changes until `brine apply PLAN_ID` (D8). `--apply` may combine the two for agents when policy allows. It still records the plan and journals the operation. The one exception is `secret set`, which stores an immutable new version that stays unreferenced (D5) until a plan binds it, so it never changes a running app on its own. P03-09 `remove` supports stateless apps only. Persistent apps are refused until P04-08 supplies immutable archival; permanent deletion remains the separate, explicitly reviewed `data purge` under current target and path authorization.

### Local validation and offline planning (P01-04)

`validate` and `plan --offline` read only local regular files. Neither performs network, SSH, subprocess, runtime, proxy or control-database operations. Both require exactly one app path and a nonempty explicit `--policy`. Specs and policies are bounded to 1 MiB each; snapshots and committed-state files to 16 MiB each. Invalid arguments, unreadable inputs, invalid specs, malformed snapshots and malformed committed state return `invalid_usage` with exit 2. Policy decode or enforcement refusals return `policy_refused` with exit 4. Diagnostics never echo input values or paths.

`validate` human output summarizes the normalized name, digest-pinned image, domains, container port, policy version and environment/secret counts. Its machine `data` is the normalized desired configuration, with `environment` replaced by a sorted array of names, never literal values. Health/resource defaults, policy hash/version, app port range and secret references remain visible.

`plan` without `--target` or enabled `--offline` refuses with `offline_required` and exit 2 before reading files. `plan --target NAME` uses connected planning on an enrolled host; the remaining details in this section describe offline previews. A supplied snapshot is never treated as a connected host. The image digest comes from the validated app; an installed app's matching digest retains its observed platform. Otherwise Linux and the snapshot architecture are explicit offline assumptions, not verified registry metadata. No manifest is fetched. The selected platform manifest observation is always `unknown`, even when an installed app supplies the platform.

`--state` reads the separate `plan.BrineState` JSON schema, including every normalized previous release input. Its decoder rejects unknown, missing, duplicate, case-aliased and null fields, as well as trailing JSON. Without `--state`, an affirmatively empty, generation-zero snapshot permits an empty bound state for a fresh-host preview. Other snapshots receive unbound empty state and produce a conflict; the CLI never invents installed release history from the new desired app. Use the example committed-state file for the installed-app no-op case.

Create, update, no-op and conflict are all successful read-only planning results, with exit 0 and `ok: true`. A conflict has `data.kind: "conflict"` and diagnostics, not executable changes. This distinguishes a completed analysis that found obstacles from a failed invocation or the category-5 refusal to reuse corrupt stored evidence. Callers must inspect `kind`, not infer deployment readiness from exit 0.

Machine plan `data` is exactly `planview.JSON`'s redacted projection, including its hash and diff, never the `planfile.Offline` envelope or retained literal environment values. `--out` does not change that projection. Human output prominently begins with `OFFLINE PLAN — NOT APPLYABLE`, renders the planview diff, and prints the complete plan hash plus a quoted file path when written.

Only `plan --offline --out <existing directory>` persists anything. It writes one private, hash-named offline plan through `planfile.Write`, without creating a directory or control database. Temporary files used for atomic publication are removed. Creation time and tool version remain outside the fingerprint. Sequential retries verify and reuse an existing hash-named, regular 0600 file without rewriting its original metadata; malformed, tampered, non-private or nonregular existing evidence refuses with `conflict` and exit 5. Other output I/O failures use exit 1. The file retains literal settings for fingerprint verification and is not a log or safe public presentation. It never grants apply authority.

## Machine modes

`--json` emits exactly one newline-terminated JSON object on stdout for each invocation, including command and flag errors. Stdout must be writable for this guarantee. Human help requested with a machine flag is returned as a string in `data.help`. Without a machine flag, commands retain human output.

`--jsonl` emits one compact JSON envelope per line. One-shot commands other than `logs` and app `status` emit exactly one line containing the final result, identical to `--json`. `logs` emits one `log` envelope per entry, then a `complete` invocation result (including an empty tail). Future streaming commands may emit progress envelopes followed by one final invocation result. Every line uses the version 1 envelope below, with event payloads inside `data`; there is no surrounding array, pretty printing, blank line or unstructured status text. This foundation defines framing, not future event names or payload schemas. A final accepted result does not mean the background operation has finished.

`--json` and `--jsonl` together are a usage error with exit 2 and one `invalid_usage` JSON envelope on stdout, even when help is requested. An explicit `--json=false` or `--jsonl=false` disables that flag; the last value of each flag wins. Flags after `--` are positional arguments. Malformed machine-flag values still get an error envelope, even when a later false value disables that flag. Machine flags are recognized before parsing so an earlier unknown flag cannot suppress the envelope.

`__complete` and `__completeNoDesc` use Cobra's shell-completion protocol. Their arguments describe another command line, so machine flags inside that command line do not change the protocol response.

`--no-input` forbids all prompts. JSON and JSONL also forbid prompts without requiring `--no-input`. Read-only commands that need no input still succeed. Commands must route every prompt through `internal/cli.RequestInput`, which checks the flags and injected input/output streams before running the prompt callback. If input is needed but forbidden, or either stream is not a terminal, it returns typed `input_required` with exit 2 before printing or reading anything. Callers must supply explicit arguments instead; no automatic answer or authorization is implied. Allowed prompt callbacks receive the injected context, stdin and stderr. The helper checks cancellation before asking. `deploy` uses this guard for its explicit combined-review confirmation.

Terminal detection uses the injected stream's `Fd()` when available, never an unrelated process stdout. Streams without a terminal file descriptor are nonterminals. Human stdout contains no ANSI sequences when redirected, even if `CLICOLOR_FORCE` is set. A nonempty `NO_COLOR` also removes styling from human command output. Styled human renderers use `internal/cli.humanTheme` to derive their theme from the injected stdout terminal and `NO_COLOR`, instead of emitting styles for redirected output. The plain-output writer is a defensive backstop. It keeps escape-parser state across writes so split escape sequences cannot leak, and preserves text bytes even when UTF-8 is invalid or truncated. It drops standalone C0 and C1 terminal controls except LF, CR and TAB, including vertical tab and form feed; it is not a lossless control-character filter. JSON and JSONL never include ANSI styling, spinners or prompts, on terminals or pipes. Safe diagnostics go to stderr and never include raw subprocess output or argument values.

`tui` refuses enabled `--json`, `--jsonl` and `--no-input` with `tui_interactive` and exit 2. It also refuses nonterminal output or input with `tui_terminal_required` and exit 2 before starting its runner. The existing process-input fallback remains allowed when stdout is a terminal, the process's own stdin is redirected, and a controlling terminal is available. Explicitly injected nonterminal input never falls back to a host device. `NO_COLOR` removes TUI text styling, not the cursor-control sequences needed for interaction. Requesting help does not start the TUI.

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

Probes use a fresh account-derived environment with fixed `/usr/bin:/bin` child PATH and no inherited runtime, configuration or loader overrides. Doctor still trusts the executable selected by its client PATH lookup; absolute executable paths are not an authorization boundary. Raw output stays private. All local runner entry points share this environment policy and process-group cancellation; host probes get account-derived user-bus variables and an accessible account-home working directory.

Human output identifies the local scope and shows each tool's requirement and version or failure reason. Failure JSON follows the shared envelope contract and carries `data: null`, `command: "brine doctor"`, and the fixed safe dependency error. It therefore contains no local report or per-tool detail. Partial results on failure, explicit local-scope fields on failures, and a safe message naming the missing tool require a later versioned contract extension.

## Error codes and exit categories

| Code | Exit | Retryable | Safe message |
| --- | --- | --- | --- |
| `logs_ownership_refused` | 4 | false | Logs are readable only for an observed Brine-owned app unit. |
| `logs_truncated` | 1 | false | The log output was truncated; request a smaller tail. |
| `logs_limit_exceeded` | 1 | false | The log response exceeds its bounded tail or byte limit; request a smaller tail. |
| `logs_invalid_journal` | 1 | false | The journal response is malformed. |
| `logs_inventory_failed` | 1 | false | Log collection failed while checking app ownership in host inventory. |
| `logs_inventory_timeout` | 1 | false | Log collection timed out while checking app ownership in host inventory. |
| `logs_journal_failed` | 1 | false | Log collection failed while running journalctl as the enrolled runner; inspect its journal access and command exit. |
| `logs_journal_timeout` | 1 | false | Log collection timed out while running journalctl as the enrolled runner. |
| `logs_journal_unavailable` | 3 | false | Unit journal logs require journalctl and readable journals. Use app logs for container output; ask the operator to inspect unit logs without widening deploy credentials. |
| `logs_container_failed` | 1 | false | Log collection failed while inspecting or reading the owned app container with Podman. |
| `logs_container_timeout` | 1 | false | Log collection timed out while inspecting or reading the owned app container with Podman. |
| `logs_container_unavailable` | 3 | false | App logs require Podman and the managed k8s-file log driver; inspect the app unit and container configuration. |
| `logs_invalid_container` | 1 | false | The container log response is malformed. |
| `persistent_archive_required` | 4 | false | Removal refused. Persistent data needs the P04-08 archive path; no data will be deleted. |
| `app_not_found` | 2 | false | The app has no committed Brine release. |
| `release_not_found` | 2 | false | The rollback release is not known for this app. |
| `native_point_not_found` | 2 | false | The native point reference is not known on this target. |
| `rollback_no_op` | 5 | false | The rollback target is already current or requires no changes. |
| `native_capture_verification_refused` | 1 | false | Native capture remote verification refused. |
| `native_capture_source_refused` | 1 | false | Native capture source recheck refused. |
| `native_capture_live_refused` | 1 | false | Native capture live engine recheck refused. |
| `native_capture_candidate_refused` | 1 | false | Native capture module recheck refused. |
| `native_capture_commit_refused` | 1 | false | Native capture verified point commit refused. |
| `internal_error` | 1 | false | The operation failed. |
| `invalid_usage` | 2 | false | Invalid command or arguments. Use --help for usage. |
| `dependency_missing` | 3 | false | A required dependency is missing or incompatible. |
| `policy_refused` | 4 | false | The operation was refused by policy. |
| `policy_registry_denied` | 4 | false | The image registry or repository is not allowed by operator policy. |
| `backup_admission_refresh_required` | 4 | false | Backup credential delivery refused: the operator policy changed. An explicit approved admission refresh is required before continuing. |
| `conflict` | 5 | true | The plan is stale or another operation holds the lock. |
| `recovery_required` | 6 | false | Manual recovery is required before continuing. |
| `interrupted` | 130 | false | The client was interrupted. |
| `tui_interactive` | 2 | false | tui is interactive; remove --json, --jsonl and --no-input |
| `tui_terminal_required` | 2 | false | tui requires terminal input and output. |
| `offline_required` | 2 | false | Choose --target NAME for connected planning, or use --offline with --snapshot and --policy. |
| `input_required` | 2 | false | Interactive input is required; supply explicit arguments or use an interactive terminal. |
| `dispatch_invalid_request` | 2 | false | The dispatcher request is invalid. |
| `dispatch_unsupported_schema` | 3 | false | The dispatcher protocol version is incompatible. |
| `dispatch_operation_refused` | 4 | false | The dispatcher operation is not allowed. |
| `dispatch_root_refused` | 4 | false | The host dispatcher cannot run as root. |
| `transport_invalid_target` | 2 | false | The SSH target configuration is invalid. |
| `transport_failure` | 1 | false | The SSH transport failed; reconcile before retrying any mutation. |
| `transport_invalid_response` | 1 | false | The SSH dispatcher response is invalid. |

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

## Bounded app logs (P03-06 logs)

`brine logs APP --target NAME --tail N [--since RFC3339]` reads the enrolled target configuration from the same client directory as `enroll`. `--config-dir` overrides that directory. Tail defaults to 100 and must be between 1 and 1000. `--since` accepts only an RFC3339 timestamp with fixed-width date/time fields, a dot for fractional seconds (up to nanosecond precision), and timezone hours/minutes in range. The runner parses it with `time.RFC3339` and passes normalized UTC to Podman as a separate argument. Journal expressions, paths and extra options are refused.

The read-only dispatcher operation is `logs`, with exact-case `args` fields `app`, `tail` and optional `since`. The runner checks inventory before reading logs. Apps with unknown ownership, no matching container unit or ambiguous matching units return `logs_ownership_refused`. Only the inventory-recorded `<app>.container` or Brine-prefixed `brine-<app>.container` is accepted. The service and `systemd-<unit-base>` container names are derived internally; requests cannot supply either name or a log path. A bounded `podman --remote=false inspect --type container --format <fixed-template> <derived-container>` returns only the immutable container ID, name, `PODMAN_SYSTEMD_UNIT` label and log-driver type. Invalid IDs, name or label mismatches refuse before reading any messages. Reading uses the verified immutable ID so replacing a container name between inspection and reading cannot select a different container. The driver must be `k8s-file`; no journal-driver fallback or global journal access is granted.

Every rendered app Quadlet sets `LogDriver=k8s-file` and `LogOpt=max-size=10485760` (10 MiB). Rootless Podman chooses its own per-container log path under the runner's container storage, and conmon creates the private file with mode 0600. The size option discards the previous file and starts a new file at the limit; it is not an archive or multi-file retention policy. Small record/header overhead and the writer's chunk size are not a hard filesystem quota. These are container-lifetime logs and disappear when Podman removes the container. Brine sets no host-wide Podman default, journal storage setting, journal-group membership or external log path.

The runner executes typed arguments equivalent to `podman --remote=false logs --timestamps --tail <tail> [--since <UTC>] <verified-container-id>`, without follow mode. A ten-second deadline covers inventory, container inspection and reading together. It does not switch users, mutate state or invoke a shell. Both stdout and stderr are app log streams on successful Podman completion. The shared executor captures at most 256 KiB per stream; their combined raw bytes and the serialized sanitized array must each fit 128 KiB. Extra records, byte overflow and capture truncation fail closed without partial logs. Truncation returns `logs_truncated`; malformed timestamped container output returns `logs_invalid_container`. No raw failure output, inspection metadata or underlying error text is presented.

The host response and client `--json` result contain an array of `{timestamp, priority, message}`. Podman 5.4's [timestamped log serialization](https://github.com/containers/podman/blob/v5.4.2/libpod/logs/log.go) is parsed as a leading RFC3339 timestamp, one separating space and the remaining message. Empty messages and a final unterminated partial line are supported. Podman's embedded timestamp prefixes on partial chunks remain part of the rendered message. Both streams are merged by timestamp, with stable stdout-before-stderr ordering for equal timestamps. Timestamps are normalized to UTC RFC3339 with available nanoseconds. Container stdout maps to priority 6 and stderr to 3; these values describe the stream, not an application-supplied syslog severity. An empty tail is `[]`. Human output renders one timestamped line per entry and escapes embedded newlines. ANSI escapes and control characters are stripped from messages before presentation.

Inventory failures and deadlines retain `logs_inventory_failed` and `logs_inventory_timeout`. Container inspection/read failures return `logs_container_failed`; deadlines return `logs_container_timeout`. A missing Podman executable or unsupported log driver returns `logs_container_unavailable`, with a fixed instruction to inspect the app unit and container configuration. Cancellation still returns `interrupted`.

Unit lifecycle logs remain a separate, ownership-checked journal source in diagnostic `unit_logs` facts. The runner executes `journalctl --user -u <derived-unit> -n <tail> -o json --no-pager --all`. Readable persistent split journals are supported. Journal execution failures and deadlines retain `logs_journal_failed` and `logs_journal_timeout`; malformed records retain `logs_invalid_journal`. A missing journalctl executable, or exit 1 with the observed fixed stderr `No journal files were opened due to insufficient permissions.`, returns `logs_journal_unavailable`. The fixed message directs the operator to inspect unit logs without widening deploy credentials. A volatile or non-split journal can be unavailable while app `logs` remain readable; failure of one source does not replace or discard the other source's result. Diagnostic facts preserve returned collection codes; a probe that expires before its reader answers remains `probe_timeout`.

`--jsonl` uses version 1 envelopes with `command: "brine logs"`. Each entry has `data: {"event":"log","line":{...}}`. The final result has `data: {"event":"complete","count":N}`. There are N entry lines plus one final invocation-result line, including one completion line for an empty tail. Validation or operational failure produces one failure envelope and no partial entry stream. The client buffers this bounded response before publishing it; this is not a live follow stream.

Redaction is **best-effort**, not a promise that unknown secret values can be identified. Brine never retrieves plaintext Podman secret values to build a redaction list. Quoted values are matched with escape awareness, including escaped quotes and backslashes. Patterns cover secret-looking assignment/JSON keys, bearer credentials, URL passwords, token-like strings of at least 32 characters, and private-key blocks (including blocks spanning returned entries). The client validates and sanitizes structured messages again. False positives are intentional. Arbitrary prose secrets, unusual encodings, or private-key tails whose opening marker is outside the requested window may evade these patterns. Applications must not log secrets.

The scrubbed Debian 13 volatile-journal fixture records the coordinator's actual exit 1 and permission-denial stderr. Failing-first tests reproduce the app-log failure with that fixture, then prove Podman app logs remain available independently of unreadable journals. Podman output fixtures follow the pinned 5.4 serialization and are synthetic, not captured deployed-app logs. The older Debian journal JSON fixture is a scrubbed system journal record because the operator's initial user-unit queries had no entries. These fixtures and generator goldens do not replace post-merge verification of a deployed app, runtime file-size enforcement or restricted dispatcher access on the device.

## App status and rollback planning (P03-06)

These endpoints use trusted `HostApps` dependencies, like the existing detached-job endpoints. The production host composition supplies the target store, inventory and policy; missing dependencies still fail closed. Tests cover the service, SQLite store and dispatcher/client chain with fixtures. They do not claim a real deployment or rollback on a host.

`brine status [APP] --target NAME` reads committed Brine apps. Omitting APP returns all committed apps, sorted by name. An empty host returns `{"apps":[]}`. An explicit app without a committed release returns `app_not_found`. The existing root `status` command still accepts `--operation ID`; that mode cannot also take APP. `--after-cursor` requires operation mode. Both modes use the same pinned target configuration and optional `--config-dir`.

The read-only dispatcher operation is `status`, with exactly `args: {"app":""}` for all apps or a validated app name. Its `data` has an `apps` array. Each record contains `app`, `current`, nullable `previous`, nullable `last_operation`, `health` and `drift`. Releases expose `id`, `plan_id`, full `image_digest`, `port` and `domains`. Human output abbreviates image digests to 12 hex digits. The last operation includes failed and unfinished attempts and uses the existing safe operation record, without requester or idempotency key. No literal environment values, secret values, Podman secret IDs or artifact contents appear.

Inventory optionally records `unit_active` for each app. It is a boolean observation from a bounded `systemctl --user show <derived-owned-service> --property=ActiveState --value` call. Old snapshots may omit it; unknown or unavailable measurements remain unknown. The planner excludes this volatile observation from decision facts, so changing service activity does not change a plan hash.

Health contains `unit_active` and `direct`. Direct health is `healthy`, `unhealthy` or `not_checked`. Status makes at most three direct probes, each with a 500 ms deadline, only for active apps with a matching measured committed port and no affirmative artifact drift. It skips probes when less than 500 ms remains in the request context. Each GET uses the committed health path on the fixed loopback listener, without proxies, credentials, redirect following or body reads. A failed attempt is unhealthy, not unchecked. Additional apps and unverified listeners are not checked. This is a current observation, not a deployment commit or routed-health proof.

Drift has `state` equal to `in_sync`, `drifted` or `unknown`, plus sorted differing `fields`. It compares app presence, image/platform, allocated port, Quadlet hashes, required immutable secret bindings, selected Caddy file hash and live served domains against the committed release. Retained extra secret versions are not drift. Domain comparisons use only live routes affirmatively attributed to this app; routes belonging to other apps or lacking app attribution never become this app's domain differences. Without an attributed route measurement, domain drift stays unknown and does not alone suppress a health probe. An affirmatively absent managed Caddy configuration or missing committed file is a `caddy` difference, meaning the managed route is missing, while an unobservable configuration remains unknown. Unknown measurements never claim equality; affirmative differences still report drift when other facts are unknown. Status saves no plans and changes no operation or release state.

App `--json` emits one report envelope. App `--jsonl` emits one `data: {"event":"app","app":<record>}` envelope per app, then `data: {"event":"complete","count":N}`. An empty host emits only completion. Operation status retains its single-envelope JSONL contract. Host responses stay bounded single JSON objects; JSONL is client presentation, not a host stream.

`brine rollback APP [--release RELEASE_ID] --target NAME` returns a plan, never an implicit apply. Dispatcher args are exactly `app` and `release_id`; an empty release ID selects the previous known-good release. Explicit IDs are looked up within the named app's immutable history. An unknown release returns `release_not_found`; the current release or a computed no-op returns `rollback_no_op`. Neither refusal writes a plan.

The service loads the selected release's exact desired input through `LoadPlan(release.PlanID)`, collects live inventory and current `BrineState`, runs the normal planner with the selected release's image metadata, and saves the resulting plan through `SavePlan`. Ownership, drift, port and secret-version selection remain the normal planner's rules. Applying still requires `brine apply PLAN_ID`, including freshness and current operator-policy checks under the host lock. The response has `plan_id`, `release_id`, `compatibility`, `kind`, the usual old-to-new `diff`, and `conflicts`. Both JSON and JSONL emit one envelope. A conflict is a completed planning result with no executable changes, not permission to apply.

Compatibility is `stateless_compatible` only when both exact desired inputs have the frozen v1 stateless shape and schema version 1, matching the executor's rule. The Go conversion deliberately stops compiling if `policy.Desired` gains fields. Any unknown or schema-boundary compatibility returns `recovery_required` before saving. Persistent-data compatibility follows the approved D12 [Schema migrations contract](#schema-migrations), with its implementation gated on P04-01. App rollback never rewinds SQLite data, restores a database, rotates release heads or launches an operation while planning.

### Config, secret and lifecycle operations (P03-08)

These commands use injected `HostConfig` and `HostSecrets` services. Production wiring and physical-host evidence remain later wave gates. Planning does not change a running app.

`brine config set APP KEY=VALUE... --target NAME` starts from the current release's exact stored desired input through `LoadPlan(release.PlanID)`. A bare key sets an environment variable; `environment.KEY` is explicit. Repeat `--unset KEY` to remove environment keys. Other accepted assignments are `resources.memory_mb`, `resources.pids_limit`, `health.path`, `health.expected_status`, `health.startup_deadline_seconds`, `health.timeout_seconds` and `secrets.ENV=REFERENCE`. `domains.add=DOMAIN` and `domains.remove=DOMAIN` edit domains. No image, executable, path, unit or arbitrary field assignment is accepted. Up to 128 edits are allowed, each key at most 256 bytes and value at most 32 KiB; the transport's total request bound still applies.

The service rebuilds a strict spec and applies the current operator policy to the final edited configuration. This permits reducing an old resource limit after an operator lowers its ceiling. It then plans against fresh inventory and committed state and saves the plan. Environment values stay only in the store's private desired input, never executable plan bytes, diffs, responses or events. Domain ownership, resource ceilings, health validation and app-scoped secret references use the same validation as a new spec.

The mutating dispatcher operation `config_set` takes exactly `app` and `edits`, with each edit containing exactly `key`, `value` and `action`. Actions are empty or `set` for assignments, `unset` for environment keys, and `add`/`remove` for domains. `lifecycle` takes exactly `app` and `action`, one of `restart_app`, `stop_app`, `start_app` or `remove_app`. Both return `plan_id`, `kind`, `diff`, `conflicts` and optional `lifecycle`. Human output shows the safe diff or lifecycle action and the apply command. JSON and JSONL each emit one envelope. A conflict plan has no effects. Apply still requires `brine apply PLAN_ID` and fresh checks under the host mutation lock.

Restart/stop/start lifecycle plans have exactly one typed action and retain the committed configuration and immutable secret bindings. A newly stored unbound secret cannot rotate a restart. The executor refuses hidden configuration edits and non-deploy operation records. `stop_unit` joins the closed step allowlist; stop journals intent before touching the owned user unit. Already-stopped is success with no stop effect. Unknown stops require manager-job settlement plus independent unit/container evidence; unresolved actions end in `recovery_required`. Start/restart reuse `reload_units`, `start_unit` and `check_direct`. Restart/stop/start never install artifacts, change routing, advance release heads or touch app data. Removal has the separate ownership-bound effects below. Lifecycle deployment-state edges add preparing-to-starting, quiescing-to-succeeded/failed, starting-to-failed and checking-to-succeeded/failed.

`brine secret set APP NAME --target NAME` reads a nonempty value of at most 32 KiB from stdin, preserving its bytes. It accepts neither a value argument nor a value flag. Dispatcher `secret_set` takes exactly `app`, `reference` and `value`; value is canonical base64 solely for stdin transport. The handler clears decoded byte buffers best-effort and never returns the value or raw runtime diagnostics. Values reach Podman only through the typed adapter's bounded stdin path, not argv or child environment.

The secret service acquires the host mutation lock and reloads app-scoped operator secret policy before creating a planless operation. It works before the first app release. Its immutable name is `brine.<app>.<ref>.v<n>`; app and reference forbid dots, so ownership splits into exactly four parts. References allow ASCII letters, digits, underscores and hyphens, beginning with a letter or digit. The complete generated name must fit 253 bytes including the largest uint64 version. Inventory parses this format without consulting the store. The typed Podman secret-name parser, planner and Quadlet renderer share this naming rule. No real host secrets exist yet, so the format change needs no secret migration.

A `secret_set` operation has empty `plan_id`, validated `app` and `secret_ref`, and no value field. Its only state edges are `queued -> preparing -> succeeded|failed|recovery_required`; detached jobs and the deploy executor refuse it. Deploy operations still require a plan ID. Idempotency is unique per authenticated requester/key across both kinds. A matching secret retry inspects the recorded assignment and never creates it again. The CLI generates a fresh correlation ID per call and does not automatically retry an uncertain response; direct protocol clients must retain the request ID to recover the same operation.

The closed `secret_version` event contains only `name` and `outcome`, with outcome `intent`, `completed` or `unknown`. The name must satisfy the exact dotted grammar and match the operation's app/reference. Allocation takes the maximum version from live Podman names and all durable assignments, including failed or uncertain attempts. It journals intent before creation, so a delayed unknown creation cannot make a later operation reuse its name. After a timeout or crash, read-only existence checks can prove completion; absent or unreadable evidence becomes recovery-required rather than a blind create retry. The returned `operation_id`, `version_name` and `bound:false` describe storage only. A later config or initial deployment plan must explicitly bind the reference before apply. Old versions stay available for retained releases.

## Restricted dispatcher and client transport (first half of P02-02)

`brine host serve` is hidden and always speaks JSON, independent of presentation flags. Command resolution selects dispatcher setup before interpreting presentation flags, including leading global flags such as `brine --json host serve`. Every argument after `serve` is ignored. It refuses effective UID 0 before reading stdin. Before clearing the environment, it captures only the authentication marker set by enrollment's root-owned forced command and the byte length of `SSH_ORIGINAL_COMMAND`. Only that byte length appears on stderr. That variable never selects an operation or contributes response text. A pipe read deadline limits incomplete requests to five seconds. Regular files, which do not support read deadlines, are read through a bounded buffer with a five-second timer and cancellation signal. Unsupported deadlines on other input types are still refused. The dispatcher separately bounds request reading to fifteen seconds and closes closable input on cancellation. Ordinary calls have a fifteen-second deadline covering request reading and processing; inventory implementations must honor cancellation. Enrollment must still enforce D7. Clearing the environment inside a binary cannot undo code run by a login shell or dynamic loader before it starts.

The dispatcher reads stdin through EOF, with a **2 MiB** bound including whitespace. It accepts exactly one UTF-8 JSON object with these required, exact-case fields:

~~~json
{"schema_version":1,"op":"ping","request_id":"example-1","args":{}}
~~~

Duplicate or unknown fields, case aliases, null fields, wrong types, missing fields, trailing data and a second request are refused before dispatch. `request_id` is caller-supplied correlation input, limited to 1-128 ASCII letters, digits, hyphens and underscores. It does not grant authority. For `secret_set` only, trusted host code scopes this ID with the authenticated requester as its idempotency key; other operations keep their own contracts. The result envelope is unchanged and does not echo this ID. Each SSH call carries one request and its one response.

The immutable operation registry declares `ping`, `inventory`, `logs` and app `status` as `read_only`. Rollback planning is `mutating` because it saves a plan in the control store, but it changes no app, unit, route or data. `ping` and `inventory` have separate typed empty argument decoders. Both require `args: {}`. `ping` returns `data: {"server_version":"<binary version>","protocol_versions":[1]}`. `inventory` calls `dispatch.Inventory.Collect(context.Context) (target.Snapshot, error)` and returns the validated snapshot. Without that provider it returns `dependency_missing`; live collection is a separate task. Connected `plan` is `mutating` because it saves verified intent. Its only argument is `{"spec":"<strict app TOML>"}`. It returns `plan_id`, `kind`, `diff` and `conflicts`, without literal environment values. `brine plan ./brine.toml --target NAME` sends this request; offline inputs and local policy flags cannot be mixed with connected planning. The registry also declares `apply` as `mutating` and `operation` as `read_only`, with the contracts below. `apply` remains refused without trusted host authorization and configured job dependencies. The production binary wires the store, executor and operator policy through `internal/host`. Missing dependencies or a missing trusted requester still refuse mutations.

Responses use the existing `result.Envelope`, with `command` equal to `brine host <operation>`. Pre-dispatch refusals use `brine host serve`. They have a **4 MiB** bound including the terminal newline. `logs` JSONL framing is a client presentation of the single bounded host response. These host operations are not streams. Future streaming operations must declare and test JSONL explicitly; callers cannot request a stream through argv. Wrong request schemas exit 3, unknown operations exit 4, malformed or oversized input exits 2, and root invocation exits 4. Other failures use the existing categories above.

`transport.LoadTarget` reads a client-side file with a **16 KiB** bound. Its required fields are `name`, `destination`, `identity_path` and `pinned_host_key`; decoding rejects unknown fields, aliases, duplicates, nulls and trailing data. Names are lowercase ASCII labels. Destinations are Host aliases or explicit `user@host` values with an ASCII username. Host and alias values contain ASCII letters, digits, underscores, dots and hyphens, without a leading hyphen, URI scheme or inline SSH options. An explicit root username is refused; the server independently refuses effective UID 0 even when an alias supplies the user. Numeric dotted host labels are accepted. Custom ports and proxy hops come from the trusted user SSH configuration, not request fields. Identity paths are clean absolute Unix paths containing only ASCII letters, digits, underscores, dots, slashes and hyphens. The initial pin format is one `ssh-ed25519 <base64>` public key without a comment, with its SSH binary structure validated. Inline IPv6 literals, other host-key algorithms and paths containing whitespace or SSH expansion tokens are not accepted in this initial transport. Host aliases can resolve IPv6 destinations and custom ports through SSH configuration.

The caller supplies an existing or newly created private `KnownHostsDir` outside the repository. Each target has a mode-0600 `<name>.known_hosts` file in a mode-0700 directory. The pin uses the fixed host-key alias `brine-pin`. Existing pins must match byte-for-byte; they are never replaced automatically. Final-component symlinks and unsafe file modes are refused. Local configuration, its parent directories and identity files are trusted operator-owned input, not a sandbox against another process running as the same client user.

The client selects the system `ssh` from `/usr/bin:/bin`, then uses `localexec` with separate arguments, request bytes on stdin, a fresh account-derived environment and separately bounded stdout/stderr. Only the explicitly selected `SSH_AUTH_SOCK` is forwarded for agent support; ambient PATH, runtime, configuration and loader overrides are not inherited. D1's trusted client SSH configuration, Host aliases, configured ports, proxy hops and agent support are preserved. Command-line safety options take precedence over scalar configuration values: `BatchMode=yes`, `StrictHostKeyChecking=yes`, the pinned `UserKnownHostsFile`, `GlobalKnownHostsFile=none`, `ForwardAgent=no`, `ClearAllForwardings=yes`, `IdentitiesOnly=yes`, `RequestTTY=no` and `PermitLocalCommand=no`. The explicit identity is supplied with `-i`; `--` precedes the destination. DNS host-key trust and key updates are disabled. OpenSSH `IdentityFile` entries are additive, so an explicit identity plus `IdentitiesOnly=yes` is not an exclusive identity allowlist. Configured identities may also use the agent. The server's restricted key and D7 protections, not the client's configuration, establish the remote authorization boundary.

Connection establishment is limited to five seconds, keepalive failure to one five-second interval, and ordinary complete calls to fifteen seconds or the caller's earlier deadline. Stdout and stderr each have a **4 MiB** bound; any overflow fails the call, even when a valid response prefix exists. Raw diagnostics are not presented. Client-generated `transport_failure` envelopes add `error.details` with the dispatcher `operation`, a fixed `reason` (`deadline_exceeded`, `output_limit`, `ssh_exit_255` or `runner_failure`), and an optional `ssh_stderr_tail`. The tail retains at most the final 2 KiB and 16 lines of captured SSH stderr, replacing every line with a fixed recognized failure phrase or `[redacted]`. It never includes remote banners, addresses, paths, fingerprints or key material. Canceled observers remain `interrupted`. Responses are decoded strictly at every declared nesting level, including inventory snapshots, and the error code, fixed message, retryability and SSH exit category must agree. Exit 255 is transport failure. Calls never retry automatically. A timeout or lost connection is not proof of a remote mutation's outcome.

`module_publish_plan`, `native_backup_inspect` and `native_backup_review` restore and read an exact remote point synchronously. After strict request validation, their host work budget is nine minutes, matching the existing native transport work limit. Their complete client call budget is ten minutes thirty seconds: fifteen seconds for request reading, nine minutes for work, seventy seconds for bounded cleanup and five seconds for connection establishment. The cleanup reserve covers the existing maximum sixty-second native stop plus ten-second teardown. Original caller cancellation and earlier deadlines still apply. Cleanup uses its existing separate bounded contexts; the reserve does not extend work. The descriptor deadline remains twenty seconds. `module_publish_inspect`, live-restore planning, status and other ordinary calls retain fifteen seconds. Point and material expiry, unknown-outcome handling and the no-retry rule are unchanged.

Operator enrollment and live restricted-key/PAM/startup-file verification are described below. The initial protocol and fake-SSH tests alone do not establish the full P06-01 production boundary.

### Detached apply and operation polling (P03-03)

These endpoints are tested with injected store/executor interfaces and fake host processes. The production host composition now supplies those dependencies and operator-policy authorization. Neither these tests nor the command's presence prove a working deployment or a real systemd disconnect drill.

`apply` requires exactly `args: {"plan_id":"sha256:<64 lowercase hex>","idempotency_key":"<key>"}`. The key is 1-128 ASCII letters, digits, hyphens or underscores, starting with a letter or digit. The requester comes from trusted host code, never JSON. The store atomically binds each requester/key pair to one plan and one operation. A matching duplicate returns that operation ID without another launch, including after a failure, uncertain launch or interrupted dispatcher. Reusing a key for a different plan conflicts. A queued record left between intent and launch requires reconciliation, not automatic relaunch.

The operation record and a typed launch-intent event precede systemd-run. Once recorded, observer cancellation does not cancel the bounded launch and journal work. Success returns `data: {"status":"accepted","operation_id":"<id>"}`, never `deployed`. A known launch failure records `failed` and a value-free failure event before returning an error. An uncertain launch performs a bounded unit-existence probe outside the observer's canceled context. A loaded unit confirms acceptance only; a missing unit might already have been collected and cannot prove failure. Unresolved launches record `launch_unknown` unless execution has already advanced. They return the recorded operation ID for polling and require reconciliation before any retry. Launch reporting never overwrites a state already advanced by the executor.

The unit name is `brine-op-<operation-id>`. Operation IDs use the same colon-free label grammar as idempotency keys. Only a store-issued operation ID reaches the fixed `/usr/local/bin/brine host run-op <id>` command. Plan hashes, executable paths, unit properties, environment and argv are not accepted from requests. The finite user service has `Type=exec`, `Restart=no`, `--collect`, a 900-second runtime limit, a 30-second stop limit, 512 MiB memory, 100% CPU quota, 128 tasks and private umask. Its working directory is the enrolled `/home/brine/.local/state/brine`; stdin and process output are disconnected from SSH. The launcher sets the account-derived runtime directory and user-bus address. Launch/probe subprocesses use the bounded local execution adapter and return typed errors without raw diagnostics.

The hidden `host run-op` refuses root and invalid operation IDs. It waits up to one minute for the host mutation lock, or the job's earlier deadline, to serialize behind another mutation. The bound is injectable through `Runner.LockWaitTimeout` for tests, never through request input. On timeout, cancellation or acquisition error, it uses a separate five-second journal context to append a value-free `lock_unavailable` failure and conditionally transition `queued` to `failed`. A state conflict causes a re-read without overwriting another runner's progress. Reusing the same idempotency key returns the existing failed operation without relaunching it. After acquiring the lock, it re-reads the operation and loads its exact plan and retained desired input from the target store, then calls the executor. Only queued or launch-unknown operations can enter this path. The executor owns freshness checking and terminal state. The runner never invents success if an executor returns without a terminal record; it journals a fixed failure code and marks failed before external effects or recovery-required after execution has advanced. Finished states and another lock holder cannot start a second execution.

`operation` requires exactly `args: {"operation_id":"<id>","after_cursor":0}`. It returns `data: {"operation":<record>,"events":[<event>],"next_cursor":<sequence>}`. Records contain ID, kind, app, secret reference, plan ID, state and creation/update timestamps; requester and idempotency key are omitted. Each poll returns at most 128 append-only events with increasing per-operation sequences. `next_cursor` is the last returned sequence, or the supplied cursor when the page is empty. Events use the closed, value-free schemas in `internal/ops`; free text, raw logs, credentials and environment values are refused. The journal remains authoritative after transient-unit collection or reboot.

`brine apply PLAN_ID --target NAME [--idempotency-key KEY]` uses the existing pinned SSH transport. Without an explicit key, it generates a random 128-bit key and prints that generated, nonsecret correlation ID on stderr before contacting the host. An explicit key is not echoed. Reuse the generated or explicit key to recover the same operation after a lost response. A fresh invocation without that key is a distinct operation, not an automatic retry. Client files stay in the enrollment target directory, with the same optional `--config-dir` override; no local operation database is created.

`brine status --operation ID --target NAME [--after-cursor N]` makes one read-only poll. Human output shows the operation state and next cursor. JSON and JSONL each emit one envelope, not a stream yet. Ctrl-C cancels only this observer's transport call. Neither command sends a stop or cancel request for the host unit.

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

A plan binds desired spec hash, target identity, observed generation, image index digest, selected platform manifest observation and platform, policy version, secret-reference versions, artifact digests, resource predicates and change summary. Identical canonical decision facts generate identical hashes. Expiry and other timestamps live outside hash material. Plan acceptance is not a general authorization grant.

### Pure planner contract, schema 1

`internal/plan.Build(Input)` accepts `policy.Desired`, `target.Snapshot`, caller-supplied pinned image metadata and `plan.BrineState`. It performs no I/O and reads no clock. The returned plan owns its memory. Treat it as immutable after hashing. Expiry, creation time, approval, transport provenance and offline/not-applyable status belong in a caller-owned envelope, never inside the hashed plan. Offline provenance must remain explicitly not applyable when P01-04 and P01-05 add their presentations and files.

`target.Snapshot` is the single inventory contract. `LiveCaddyFiles` records the domains actually served by each root or imported file, including files outside the Brine generation. Each file has a stable identifier, an observed app association and an explicit domains observation. Exact Brine-rendered files in the measured current generation use their filename; other files use opaque identifiers. Inventory first proves whole disk-adapted/live JSON equivalence. Before attributing generation files, it applies the same D4 root lexer as enrollment and publication: the fixed generation import must be unique, top-level and a standalone physical line. Imports inside snippets, site blocks, named routes or imported files cannot establish app provenance. Inventory then matches each standalone import's complete route occurrences against the whole configuration. Unmatched occurrences remain root-owned, including overlapping domains and catchalls. An app association requires the selected generation's measured file hash and byte-for-byte reconstruction with the Brine renderer, not just a filename. Unresolvable source imports or unmatched routes leave live provenance unknown. Planning still binds that observation to the committed release's filename, hash and domains. `PortOwners` records app associations, process names and systemd units for bound listeners. Empty app associations mean unrelated or unattributed software, never ownership permission. A used existing app port without affirmative ownership conflicts. Domain ownership is never guessed from a filename. Known empty arrays are affirmative observations, not defaults; unknown required observations conflict.

Listener inventory covers IPv4 and IPv6 on every local address, including wildcard and non-loopback binds. `UsedPorts` includes TCP `LISTEN` sockets and UDP `UNCONN` sockets with an unspecified peer (`*:*`, `0.0.0.0:*` or `[::]:*`), never connected client sockets. `PortOwners` contains TCP listener ownership only. An unconnected UDP client bind cannot be distinguished from a UDP server by these kernel facts, so it conservatively occupies its port. UDP occupancy must not be explained away by ownership of a TCP socket on the same port.

Deployment admission and its decision hash use one port projection. It preserves observation status but retains only the inclusive desired `app_ports` range, fixed Caddy HTTP/HTTPS/admin ports 80, 443 and 2019, and the planned app's observed or committed port if retained outside a revised range. Caddy enrollment independently binds ports 80 and 443; host policy requires HTTPS port 443. Both `UsedPorts` and `PortOwners` use this scope. Unconnected UDP client churn outside that scope remains visible in raw inventory but cannot stale a plan. A new TCP or UDP listener anywhere in the allocation range changes freshness and prevents allocation of that port. Future UDP app publication must retain this admission evidence and add protocol-specific ownership proof rather than hiding UDP.

Committed release state is separate from inventory. `plan.BrineState` comes from the target control database under D1 and binds its target identity and generation to the snapshot. A mismatch produces a `stale_brine_state` conflict. Its non-null `Releases` array contains `CurrentRelease` records with the release ID, previous normalized desired configuration, expected image, stable host port, immutable secret bindings and committed Quadlet/Caddy artifact digests. A known empty release array means no committed releases. Inventory app records no longer contain current release IDs. Missing state is not fabricated from new desired inputs.

Host-port admission reserves every committed release's stable port, even when its app is stopped, failed, absent from runtime inventory or has unknown runtime facts. A known observed port also remains unavailable, including a port that differs from the committed reservation. Unknown or unsupported port facts for another committed app do not block new-app admission; unknown port facts for an unrecorded app still refuse. These reservations are admission evidence, not running/stopped evidence or replacement authority. Planning changes to the failed app itself still requires its own measured port, image and artifacts; the committed reservation cannot fill those unknown facts. Only retirement of the live app record releases its reservation.

Data preparation alone creates no host-port reservation. An incarnation with no release history is omitted from durable port ownership; any observed namesake runtime still requires affirmative absence evidence. A settled archive retirement of a published release binds control inventory to that release's saved deployment target, just as a settled app-removal receipt does. Archive-only history needs no unrelated removal receipt to prove absence. Inventory clears the retired port only after proving empty owned Quadlets, no namesake or owned container, and no unexplained TCP or UDP remnant on its historical ports. Preparing a fresh incarnation under the same app name does not reactivate the old reservation. Unreadable or contradictory runtime evidence remains unknown and refuses that app's own plan.

App Quadlets use `SuccessExitStatus=143` together with `RestartForceExitStatus=143` and `Restart=on-failure`. Podman's monitor reports a container terminated by SIGTERM as numeric exit 143. During an explicit systemd stop, 143 is clean and systemd suppresses automatic restart, leaving the service inactive rather than failed. A spontaneous exit 143 still forces restart, with repeated crashes reaching systemd's start limit and a failed unit. Other nonzero exits remain failures. Every persistent-app restart repeats `writer-permit` and `writer-attempt` before the writer starts. Quiescence still holds its durable fence and requires settled manager jobs and no live writer; a queued restart cannot bypass these gates. No `reset-failed` repair is performed.

The host replica service directly execs pinned Litestream 0.5.17, without Podman's exit-status translation. Litestream catches SIGTERM, closes replication and exits zero on successful shutdown; shutdown errors exit nonzero. Its unit keeps `Restart=on-failure` with no additional successful numeric exit codes, and every restart repeats `replica-permit` and the post-lock permit check. App stop does not stop or restart this independent service.

A secret-only app inventory record does not grant authority to replace a Caddy file. Replacing an existing namesake generation file requires a matching committed Caddy artifact record. The live app association, served domains, generation file hash and committed artifact must agree; otherwise the planner emits a conflict with no changes. This includes drift between the selected files on disk and the config actually served by Caddy.

Observed addresses are normalized at the target boundary. Plain hostnames, HTTP(S) authorities and optional numeric ports compare by lowercase hostname without a terminal DNS dot. IPv4, IPv6, wildcard hostnames and catch-all listeners remain explicit claims. Schemes and ports do not exempt a hostname from ownership conflicts. Credential-bearing URLs, paths, queries, fragments and invalid hostnames are rejected. Duplicate canonical domains in a file are rejected. Wildcard claims cover their immediate subdomains; catch-all claims cover every desired domain.

Deployment requires known systemd 257, Podman 5.4, Caddy 2.6 and a packaged passt revision, known-true cgroup v2 and lingering, and a known non-root runner user. Distribution revision suffixes are accepted within those pinned version lines. Passt uses the Debian `0.0~git<date>.<revision>` package-version family, with optional distribution suffixes. Missing required runtimes, unknown readiness, unsupported observations or incompatible versions/capabilities produce conflicts with no changes. Litestream remains optional for the v1 app spec, which requests no persistence or backup behavior.

Operator policy accepts the top-level `minimum_free_disk_bytes` integer, default `1073741824` bytes, or 1 GiB. An explicit value must be positive and fit a TOML integer. Policy canonicalization and `policy.Desired` retain the resolved minimum, so changing it changes the policy and desired hashes. `Desired.CanonicalBytes()` resolves an omitted zero Go field to the same default without mutating its input. Known free disk at or above the minimum is sufficient. Known free disk below it produces `insufficient_disk` at `free_disk_bytes`; unknown or unsupported disk produces `unknown_facts` or `unsupported_target` at that field. Every disk conflict has no changes, including for an otherwise no-op plan. This minimum is a reserve floor, not an image-size estimate or a promise that later writes cannot run out of space.

The planner uses `plan.Image`, separate from inventory's `target.Image`. Its fields serialize in order `digest`, `platform`, `manifest_digest`. `digest` names the exact index digest pinned in the desired image, or the single-image digest when no index exists. `platform` records OS and architecture. `manifest_digest` is an explicit `target.Observation[string]` for the selected platform manifest. Its only accepted forms are `{"status":"known","value":"sha256:<64 lowercase hex digits>"}` and `{"status":"unknown"}`. Missing status, absent/unsupported status, a known observation without a valid digest, and an unknown observation with a value are input errors. The same shape and validation apply to committed release images, pull-image changes and image diffs. Unknown is valid preview evidence, never apply authority. The pure planner never fetches a manifest or infers its digest from the index digest. For a single-image pin, a verified manifest digest can equal the pin. Missing metadata or a different digest is an input error, not permission to resolve an image online. A platform mismatch produces a conflict. Secrets bind to the highest positive numeric version with the exact name `brine.<app>.<ref>.v<n>` in that app's inventory. Bindings include the environment name, logical reference, immutable Podman name and Podman ID. No secret values are accepted or resolved. Precreated secrets can live in an app inventory record with no committed release in `BrineState`.

Running-app inventory verifies the container's running state, exact name, systemd ownership label and image ID against the unit's selected manifest pin and index pin. Running and stopped observations share the local stored-image verifier. Collection is fully local and never requests a registry manifest. Repository-qualified local digest associations must prove both pins against the same image ID and platform. This covers both single-manifest pins (identical index and selected pins) and multi-platform index/platform aliases. It keeps a healthy app's image observation independent of registry latency and of the different request and detached-worker time budgets. If the index association is missing, inventory remains unknown even when a registry manifest could resolve it; insufficient or contradictory local evidence stays unknown. Image lookup never treats a generated unit comment or committed release as measured image evidence.

Stopped-app lifecycle inventory requires an exactly inactive Brine-owned container unit, validated pinned index/manifest references and platform, and local Podman image lookups whose stored IDs, platform and repository-qualified digest associations agree. The stored primary digest need not equal the lookup reference: index and platform pins may alias the same image. If local metadata does not retain the index association, inventory remains unknown without contacting the registry. Unrelated repositories or unresolved/contradictory pins remain unknown. A retained container must be stopped and agree with the unit name, systemd ownership label and stored image ID; a proven absent container is also allowed. Transitional, failed, contradictory or unreadable evidence remains unknown. The validated unit's loopback publication retains its allocation without claiming a live listener. Any foreign listener on that port conflicts; an inactive unit cannot claim ownership of a listener. Start and repeated stop plan against these independently observed facts and still revalidate artifact/image/port agreement with the committed release at apply preflight.

Port allocation keeps an existing app port, even when a later policy range changes. Otherwise it selects the lowest policy-range port absent from listeners, used ports and all app reservations. No-op requires a matching committed configuration fingerprint, the expected image, the app's container unit and Caddy file, and unchanged recorded artifact hashes. The previous configuration fingerprint is computed from the committed release, never from the new desired input. A secret-only record with no committed release permits creation only when it claims no unrelated runtime or Caddy artifacts. Recorded artifact drift conflicts.

Changes have typed payloads in this order: allocate a port if needed, pull and verify the image, bind existing secret versions in environment-name order, render Quadlet, stage the complete next Caddy generation, restart the app. The Caddy payload preserves every unrelated file and replaces only the owned app file. Before staging any whole-generation change, the planner checks every committed Caddy artifact against the selected generation and live file observations, including apps not being changed. A missing file, changed hash, wrong live app association or changed served domains produces an artifact-drift conflict with no changes. Matching committed files and unmanaged files remain in the preserved set. Staging is intent, not publication. Later apply phases own whole-config validation, quiescing, health checks, activation, publication and rollback. A conflict has no changes.

Fingerprints use lowercase `sha256:<hex>` over compact Go `encoding/json` output. There are no maps in hash material. Set fields sort on defensive copies. Desired domains sort lexically; desired environment and secret entries sort by environment name. Snapshot apps, units, secrets and Caddy files sort by name; used ports sort numerically. Live Caddy files sort by identifier and their normalized domains sort lexically. Port owners sort by port, app, process and unit. Brine releases sort by app; previous desired sets use the desired sort order, units sort by name and secret bindings sort by environment name. Diagnostics sort by code then field and duplicates collapse. Changes retain execution order.

- `DesiredHash` hashes exactly `policy.Desired.CanonicalBytes()`, without a trailing newline. This includes the normalized app, policy version, policy hash, port range and resolved minimum free disk bytes.
- `ConfigHash` hashes the JSON object with fields in order `desired`, `image`, `port`, `secrets`. `desired` is the canonical desired JSON object, not an encoded string. The other fields contain the expected image including the selected platform manifest observation, selected host port and ordered immutable secret bindings. The same computation over `CurrentRelease.Desired`, image, host port and secret bindings reconstructs the previous committed fingerprint for no-op comparison.
- `Hash` hashes the JSON object with fields in order `desired`, `snapshot`, `brine_state`, `plan`. The first three are canonical JSON objects. `desired` is the same input used for `DesiredHash`. `brine_state` includes its target binding, generation and every committed release field, including previous normalized configuration and artifact digests.
- `snapshot` is an explicit decision-fact projection of the validated, sorted inventory, not the complete `target.Encode` result. Its fields appear in the order listed below. Included observations retain their exact status and value, except that free disk becomes a predicate. App ownership observations are scoped to the planned app; other apps retain the port observations used for admission.
- `plan` is the complete output with its `hash` field set to the empty string. It includes schema, kind, app, target identity and host-key fingerprint, observed generation/status, policy version/hash, desired/config hashes, expected image/platform and selected platform manifest observation, host port, secret bindings, ordered changes, the optional typed `diff` and diagnostics. The final output replaces the empty field with the computed fingerprint.

The snapshot projection hashes these facts:

| Field | Exact hash material | Reason |
| --- | --- | --- |
| `schema_version` | Inventory schema version | Binds the meaning of the facts. |
| `identity` | Target ID and full host-key fingerprint | Prevents reuse on another host. |
| `os` | OS ID and version | Binds reference-platform support. |
| `arch` | Architecture | Binds target and image compatibility. |
| `versions` | Full systemd, Podman, passt, Caddy and Litestream observations | Binds installed versions and observation status. Optional Litestream still remains an exact dependency fact. |
| `cgroup_v2` | Capability observation | Binds runtime readiness. |
| `runner` | User and linger observations | Binds the execution identity and boot behavior. |
| `generation` | Control generation observation | Detects committed control-state changes. This is a decision counter, not a volatile usage counter. |
| `caddy_config` | Observation, selected generation, every file name and full file hash | Detects route-artifact drift and preserves unrelated files. |
| `apps` | Outer observation and every app's name and allocated-port observation; only the planned app also includes image digest/platform, Quadlet name/hash and secret name/ID observations | Binds the planned app's ownership, image and artifacts, while other apps contribute port admission facts only. Their committed reservations and domains remain bound in `brine_state` and routing facts. |
| `used_ports` | Observation and the port-admission projection described above | Binds relevant allocation conflicts without unrelated client-socket churn. |
| `live_caddy_files` | Observation and every file identifier, app association and served-domain observation | Binds live routing ownership, including unmanaged imports. |
| `port_owners` | Observation and each TCP listener in the same port-admission scope, with port, app association, process name and unit name | Requires affirmative listener ownership. |
| `sufficient_disk` | Observation with the same status as `free_disk_bytes`; when known, value is `free_disk_bytes >= desired.minimum_free_disk_bytes` | Binds the disk safety decision without treating harmless byte fluctuations as drift. |

Raw `free_disk_bytes` and optional app `unit_active` are volatile measurements. Raw disk bytes and unit activity are not hash material. Other apps' image, unit and secret observations are also excluded because they do not decide this app's effects or admission. The planned app retains those observations, including their unknown status, for deployment, lifecycle and removal plans. A change to its image, allocated port or owned unit hash still changes the plan hash and refuses stale apply. There are no snapshot timestamps, uptimes or usage counters. Control and Caddy generations remain hashed exactly because they identify state changes. Inventory and retained offline inputs still contain the raw disk measurement for inspection; the plan hash authenticates its decision class, not its exact byte count. Changes within either sufficient or insufficient disk class preserve the fingerprint. Crossing the minimum changes the fingerprint and, below the minimum, produces a conflict.

The Quadlet desired payload has an empty `environment` array and a separate sorted `environment_keys` array. Literal environment values appear in the transient input hash material but never in the returned plan. Changing a literal still changes the desired/config/plan hashes. Canonical plan serialization adds one terminal newline, which is not hash material. Both `ConfigHash` and `Hash` bind the manifest observation status and, when known, its full digest. Changing the manifest digest or changing unknown to known changes both hashes. Observation pointer identity is not hash material.

### Plan presentation JSON

`internal/planview.JSON(plan.Plan)` returns the compact JSON value for a future CLI envelope's `data` field. It does not write an envelope, ANSI styling, or a trailing newline. This is a redacted presentation, not the canonical plan file or hash material. The plan hash still identifies the original immutable plan. Offline provenance and not-applyable status remain the caller's responsibility.

Top-level fields appear in this order: `schema_version`, `kind`, `app`, `target`, `observed_generation`, `policy_version`, `policy_hash`, `desired_hash`, `config_hash`, `image`, `host_port`, `secrets`, `changes`, optional `diff`, `conflicts`, `hash`. Their types match the plan model except for the redacted arrays below. Empty arrays are `[]`, never `null`. Index and known platform manifest digests remain complete in JSON. Unknown platform manifests retain `manifest_digest: {"status":"unknown"}` without a value.

- `secrets` contains only `environment` and logical `reference`, in that field order. Entries sort by environment name, then reference. Podman IDs and immutable version names are omitted.
- `changes` retains the planner's execution order. Each entry starts with `kind`. Optional nonzero fields follow in the fixed order `host_port`, `image`, `environment`, `reference`, `container_port`, `environment_keys`, `domains`, `previous_generation`, `next_generation`. Empty optional arrays are omitted. Each change includes only the fields relevant to its kind. Secret bindings contain names only; Quadlet changes contain the container port and sorted environment keys, never values; routing changes contain sorted domains, host port and generation numbers. Preserved Caddy file contents and artifact records are omitted.
- `conflicts` contains `code` and `field`, in that order. Entries sort by code, then field. No raw errors or host command output are included.

`internal/planview.Human` renders the same projection through the supplied `internal/ui.Theme`. Callers select a plain theme for non-terminal output and honor `NO_COLOR` when selecting a terminal theme. Lines wrap at the requested width, capped at 80 columns, including at 40 columns. Every human plan shows `Platform manifest` as a shortened known digest or `unknown`. Image diffs show the manifest alongside the index and platform. Index and platform manifest digest abbreviations use at least 12 hex digits and extend until distinct from other digests in the presented plan. They identify an image within this output, not across a registry. JSON retains the full digest for exact identification. Control characters in human labels are replaced with spaces.

### Typed configuration diff

`plan.Plan.diff` compares desired settings against the previous committed release from `BrineState`, not guessed runtime settings. It is absent for no-op and conflict plans, and for updates that change only policy metadata. Creation records additions with `from: null`. Fields appear in order `image`, `domains`, `host_port`, `container_port`, `resources`, `health`, `environment`, `secrets`. Unchanged optional fields are omitted. The complete diff is part of the plan hash material and both renderers use it directly.

Scalar changes have `from` and `to`, in that order, with the original typed image/platform, port, resource or health structures. Domain changes have sorted `added` and `removed` arrays. Environment changes have sorted `added`, `removed` and `changed` arrays containing keys only. Values are compared transiently; neither old nor new values are retained in the plan. The diff's always-present `secrets` array sorts by environment name. Each entry has `environment`, `from`, `to`; secret versions contain only `reference` and `version_name`, never Podman IDs or secret values. A removed secret has `to: null`.

Human output orders configuration changes by the fields above, then lists operations in planner order. It uses `+` for additions, `~` with `old -> new` for changed scalar settings and secret names/versions, and `-` for removals. Domain additions and removals stay distinct rather than pairing unrelated names. Environment lines say added, removed or changed without values. Routing generations show their recorded old/new values. No literal environment setting is available in a serialized plan; a future apply implementation must obtain the normalized desired configuration from authorized control state and verify its hash before constructing runtime artifacts.

The target identity, observed generation and policy version/hash are explicit preconditions for a later apply under the host lock. P01-03 records and fingerprints these preconditions; it does not implement freshness checking or authorize mutation.

### Connected apply validation (P03-02)

Freshness uses replan-and-compare under the host mutation lock. Apply loads the stored canonical plan and exact normalized desired input, collects live inventory, and loads the host's current operator policy and `plan.BrineState`. After verifying the desired input against that policy, it reruns `plan.Build` and proceeds only when the resulting hash equals the recorded plan hash. A mismatch is a stale-plan refusal in exit category 5, not permission to replace the plan. The store verifies `DesiredHash` and a separate digest of the canonical plan bytes on save/load. Those checks detect changed stored bytes; they do not prove freshness or operator authorization. Control schema 10 also retains the exact canonical decision-input document used to compute the plan hash, atomically with the plan. It contains normalized desired input, projected snapshot facts, committed release state and the unhashed plan. The input document is private control data, bounded to 16 MiB, immutable and verified against the plan hash on load. It is not a public presentation or a replacement for fresh inventory.

When a detached preflight detects changed canonical plans, it records a `plan_drift` event before refusing effects. Its payload contains `paths`, `changed` and `truncated`. Paths contain only schema-owned field names and numeric array indices. Dynamic map keys, all old/new values, host paths and literal settings are omitted. At most 24 paths of 160 bytes each are reported within the existing 4096-byte event limit; `changed` counts all differing fields and `truncated` reports omitted paths. The existing failure state and `stale_plan` event remain unchanged. Diagnostic failures do not authorize effects. Planning and preflight compare the same bounded capacity and admission facts used by the hash, not raw reporting measurements.

Changes to ownership, versions, ports, generations, artifact hashes, secret versions, image evidence or policy require a new plan. Disk-byte fluctuations alone do not require replanning while the sufficient-disk predicate and its observation status remain unchanged. Crossing below the recorded policy minimum refuses before mutation.

Under the host mutation lock, connected apply must refuse a plan whose selected platform manifest digest is `unknown`, before any runtime mutation. A connected planner must resolve and verify the index digest, selected platform and manifest digest, then record a new immutable plan. Apply must verify both digests and platform against the runtime or registry evidence. `podman inspect` reporting the index digest alone does not verify the selected platform manifest. Apply must not fill in an unknown digest or silently rehash an existing plan. Offline envelopes remain non-applyable even if a caller supplied cached known manifest metadata. The connected host composition implements this check before calling the executor.

### Offline plan files, schema 1

`internal/planfile.New(plan.Input, Metadata)` computes an offline plan without I/O or a clock. The opaque `Offline` type owns its bytes and exposes constant `Applyable() == false` and `Reason() == "offline"`. It has no approval transition, writable provenance fields or conversion to apply authority. `Plan()` returns a defensive copy of the pure intent, not an authorization. The zero value cannot be written.

The JSON envelope contains `schema_version`, canonical `plan`, `hash`, `applyable: false`, `reason: "offline"`, `snapshot_identity`, `snapshot_generation`, `inputs` and `metadata`. `inputs` retains the supplied normalized desired configuration, complete snapshot and caller-supplied committed release state so readers can rerun `plan.Build` and verify its existing hash contract. This is offline evidence, not a client control database. The envelope does not change the planner fingerprint. `metadata` contains required caller-supplied `created_at` and `tool_version`, outside that fingerprint. Creation times serialize in UTC. A different timestamp or tool version preserves the plan hash but makes different file content, so a same-name rewrite refuses it rather than discarding either creation record.

`Read` and `Write` require caller-controlled paths and directories. They are not hardened against an attacker replacing directory entries during pathname-based operations. `Write` requires an existing directory and uses `<lowercase hash hex>.plan.json`. It writes a mode-0600 temporary file in that directory, fsyncs and closes it, then publishes atomically without replacing an existing name. Linux uses `renameat2(RENAME_NOREPLACE)`; other platforms use a same-directory hard link and remove the temporary name. The directory is fsynced after publication. Identical bytes in an existing regular mode-0600 file are idempotent. Different bytes, symlinks or unsafe file modes produce `ConflictError`. Prepublication failures remove the temporary file. A directory-sync error after publication leaves a complete file and returns an error; an identical retry is safe. Unsupported no-replace publication fails closed rather than falling back to overwrite-capable rename.

`New`, `Decode`, `Write` and `Read` share the 16 MiB limit on the exact bytes persisted, including the terminal newline. `Decode` checks both input size and canonical output size because JSON escaping can expand content. It rejects duplicate keys, unknown fields, noncanonical field-name casing at every nesting level, trailing JSON, unknown envelope or plan schemas, absent or forged offline markers and missing creation metadata. It recomputes the plan from the retained inputs and compares the complete canonical plan, hash and snapshot bindings. `Read` also verifies the filename. Corrupted content returns `IntegrityError`; unknown versions return `SchemaError`; malformed input returns a fixed `DecodeError` that does not echo values. The schema accepts secret references and immutable version IDs, never a secret-value field. Literal environment settings remain governed by the existing spec and policy contract; the file layer does not guess whether arbitrary text is a credential. No file checksum authenticates creation metadata, and these files never grant permission to apply.

Use a small SQLite control database **on the target**, owned by the runner user, for plans, releases, operations and append-only ordered events (D1). Clients keep only target configuration. It is **separate** from app SQLite databases and needs its own recovery story. Use a bounded connection pool, transactions around local state only, and migrations. Choose a maintained SQLite Go driver in a deliberate dependency PR.

A deployment writes intent and operation state durably *before* changing files, Quadlet units, containers or proxy routes. Each tool boundary has a typed adapter with bounded outputs and timeouts. Acquire a per-host lock **before** checking plan freshness; one mutation per target. Reconcile observed state after crashes: neither subprocess exit status nor client timeout alone proves an external change succeeded or failed.

### Target control store (P03-01)

`internal/store.Open(stateDir)` opens `control.db` in an existing private runner state directory. The Linux-only store refuses a symlink database or lock file. Before opening SQLite, the store securely opens or creates the database, WAL and shared-memory files without following symlinks, verifies effective-user ownership, and enforces mode 0600. It also verifies state-directory ownership and enforces mode 0700. Foreign-owned files or directories refuse. The pinned pure-Go driver is `modernc.org/sqlite` v1.60.1, so Linux amd64 and arm64 builds need no cgo. Each connection enables WAL, `synchronous=FULL`, foreign keys and a 5000-ms busy timeout. The pool holds one connection. Immediate transactions serialize local writes across processes; no transaction performs runtime, network or filesystem-adapter operations.

Schema version 2 includes plans, typed operations (`deploy`, `secret_set`, `reconcile`), ordered events, immutable releases and current/previous release pointers. Its forward migration rebuilds the operation/event foreign key together, retaining event bytes and sequences. Planless reconciliation receipts retain `kind=reconcile`, empty app/reference fields and a NULL plan reference; secret assignments use `kind=secret_set`, app/reference identity and a NULL plan reference. The migration checks foreign keys inside its transaction and restores enforcement before returning. Existing v1 rows become `kind=deploy`, with app identity read from their retained plan. The version update and table rebuild commit in one transaction. Operation identity is immutable in SQLite. Migrations, including their version update, commit together. A database from a newer schema refuses to open. Plans store canonical plan bytes, their content digest, exact normalized desired bytes with literal app settings, and `DesiredHash`. Secrets remain immutable version references, never resolved secret values. Save/load mismatches return `store.IntegrityError`; incompatible content at an existing ID refuses replacement.

The shared `internal/ops` package owns `State`, `Operation`, `Event` and `Release`; `internal/store` exports aliases. Every method takes `context.Context` first. `SavePlan(ctx, plan, desired)` returns a plan ID. `LoadPlan(ctx, id)` returns `plan.Plan`, verified `policy.Desired` and an error. `CreateOperation(ctx, ops.Intent, requester, idempotencyKey)` returns an operation, an existing flag and an error. The requester comes from trusted host code. An idempotency key is unique per requester and cannot be rebound to another operation kind, deployment plan or app-scoped secret reference.

`SetOperationState` enforces `ops.CanTransitionFor(kind, from, to)` in both Go and SQLite. An identical state retry is a no-op. `TransitionOperation(ctx, id, from, to)` compares the current state to `from` and applies a legal transition in one transaction. A mismatch or illegal edge returns typed `ops.StateConflictError` with `Current` state and the `ops.ErrStateConflict` sentinel and changes neither the record nor its events. Competing launch and execution code uses this conditional method rather than a read followed by an unconditional state update. Every transition and its state event commit together. `launch_unknown` records an uncertain launch; reconciliation must inspect it rather than automatically relaunch. Terminal states cannot transition. Verified no-op plans can go from `preflight` to `succeeded`. A known failed commit can go from `committing` to `rolling_back`; an unknown outcome goes to `recovery_required`.

`AppendEvent` assigns the next per-operation sequence in a transaction. It refuses `state` events; only a checked store transition can write them atomically with its state change. Non-state event kinds cannot carry a `State` field. SQLite refuses event updates, deletions and sequence gaps. Payloads have a 4096-byte limit and closed, exact-case schemas for `state`, `step`, `failure`, `launch` and `secret_version`. A secret-version event must match its secret operation's app and reference; deploy operations cannot emit it. Duplicate fields, arbitrary text, logs, environment values and secret values refuse. `EventsAfter` returns ordered cursor pages of at most 1024 events. `GetOperation` and `ListUnfinished` support polling and startup reconciliation; the latter excludes all terminal states, including `recovery_required`.

`CommitRelease` atomically inserts immutable artifact history and advances current/previous pointers. An identical release retry never rotates those pointers, including a late retry for an older release. Release records reference their plan for the verified desired configuration. `CurrentRelease`, `PreviousRelease` and `LoadBrineState(ctx, targetIdentity, generation)` read a consistent transaction. The reconstructed state has the exact `plan.BrineState` shape with a non-null, app-sorted release array. It refuses releases bound to a different target identity.

`AcquireHostLock(ctx)` opens the fixed `mutation.lock` beside the database and acquires kernel `flock` before freshness checks. Its returned `Lock.Release()` is idempotent. Cancellation bounds waiting. The kernel releases a dead process's holder immediately, including after SIGKILL; no PID reuse, boot-ID check or lease-expiry heuristic is needed. The lock file is never unlinked. The lock remains independent of connection or store lifetime.

## Release behavior

For MVP accept brief interruption: pull/verify a new image, stage immutable release/unit material, announce maintenance, ensure old writer stopped, activate new release, start service, check direct and routed health, publish route, record success. Maintain previous known-good immutable artifacts. If safe, restore previous app release on failure; if migrations or data compatibility make rollback uncertain, stop and mark `recovery_required`.

Use state labels such as `queued`, `launch_unknown`, `preflight`, `preparing`, `quiescing`, `starting`, `checking`, `committing`, `rolling_back`, `succeeded`, `failed`, `rolled_back`, and `recovery_required`. Persist ordered events with monotonically increasing per-operation sequence IDs. Do not perform network or host operations inside a long SQLite database transaction.

**App rollback never rewinds SQLite.** v1 does not perform uncontrolled startup migrations or auto-restore databases. Background/worker overlap is not supported yet.

## Runtime ownership and authorization

Enroll each host with affirmative operator action and a read-only inventory. Refuse conflicts with existing software, ports, domains and volumes; never uninstall other software because a plan mentions a proxy (D4). Use a dedicated rootless Podman service user and systemd user units. Test Quadlet install/reboot semantics on the pinned distribution, not merely against a local mock.

Caddy is the host's packaged service. Brine owns only its generation directories under `/etc/caddy/brine/`, validates the complete candidate config before each change, reloads through a polkit-scoped `systemctl reload caddy.service`, serializes changes, detects drift by hash, and preserves all unrelated site configuration (D4). Apps never reach the admin API. Before publication, generation directories explicitly receive mode 0755 and generated app route files 0644 so the separate Caddy service account can read them even when the writer's umask is 0077. Complete root candidate files stay private 0600 because they may contain unrelated operator configuration; detached jobs retain their private umask.

Rootless does **not** mean safe for an untrusted agent. A deployment identity that can SSH freely and control Podman can bypass Brine checks and can alter its own app data. Autonomous production mode requires an operator-owned policy and the restricted dispatcher (`brine host serve` as a forced SSH command, D1), whose permissions exclude root, host deletion, raw Podman/Caddy control and cloud teardown.

## Database and recovery

SQLite data, WAL and related files live in durable per-app directories independent of release images. Run **one independent Litestream replicator per database/destination** (matching a tested Litestream release), with separately scoped S3-compatible storage credentials and destination paths. Replication is asynchronous and may lose recent writes when the VPS dies. Do not start competing replicas writing the same destination.

`restore test` writes into a new isolated directory or disposable fixture, checks `PRAGMA integrity_check`, verifies expected application invariants and reports recovery-point information if available. It **never overwrites the production DB**. Until P04-09 ships, live restore stays an explicitly approved operator runbook; after that it is the policy-gated `restore live` plan (D8). Database storage or backup deletion is never part of image cleanup.

## Persistent data and replication

**SQLite foundation with remaining Phase 04 acceptance gates.** D11 governs the implemented identities, observation, preparation, replication and isolated verification below. Phase 04 tracks remaining lifecycle and physical evidence. Existing schema-1 stateless apps remain valid.

### Typed engine boundary (P09-01)

Current database declarations, bindings, schema definitions/observations, restore points and verification requests/receipts are concrete SQLite types. Their canonical JSON fields, field order, plan hashes and receipt bytes remain unchanged. Existing retained records decode as SQLite because their schema is a concrete SQLite record, never because a filename ends in `.db`. No historical plan or immutable evidence is rewritten and no store migration is needed for this extraction.

`data.EngineDeclaration` and `data.EngineObservation`, and `restore.Source`, have closed `sqlite` and `spacetimedb` variants. Their tagged JSON requires `engine` plus exactly that engine's payload. Unknown, duplicate, missing, null, case-aliased and mismatched fields refuse. Concrete SQLite decoders also refuse foreign engine tags. The SpacetimeDB declaration names an owned engine tree and backup reference without a SQLite filename or mount path. Its schema evidence binds native module identity and schema hash without a SQLite catalog. Its restore source names an encrypted repository snapshot and member manifest without LTX positions or SQLite checks. These are structural records, not deployment or mutation admission.

The existing TOML declaration defaults to SQLite at parsing. An explicit
`engine = "sqlite"` produces the same normalized input and canonical desired/plan
bytes. Managed `engine = "spacetimedb"` declarations use the separate
[managed deployment contract](#managed-spacetimedb-deployment-and-lifecycle-p09-04)
and protected native operations. Unknown engines refuse before effects.

SQLite observation, snapshot capture and isolated verification use bounded
engine dispatch to their fixed adapters. The operation owner retains fence,
writer-quiescence, journal and source-identity checks. SpacetimeDB uses the
[protected native capture flow](#native-backup-command-execution), whose
capabilities require actual owned source, repository, material and replay proof.
The original P09-01 extraction did not admit those native effects; subsequent
composition does not complete their physical acceptance.

P09-02 supplies the qualified `internal/spacetimedb` primitives consumed by the
native composition. `CanonicalSchemaV10` returns immutable bounded schema
evidence using `spacetimedb-v10-expanded-v2`. Named declaration sets are sorted
after positional type-reference expansion; ordered columns, variants, parameters,
index columns and default bytes remain ordered. Unknown sections, recursive or
invalid graphs, duplicate declarations and budget overflow refuse. Its versioned
schema hash supplies the derived engine marker independently of a game's marker
table.

`ObserveCurrent` accepts a validated literal-loopback location, privately resolved publisher credential, expected native database/publisher/name binding and bounded deadline. Location validation is not ownership or startup authority. It performs fixed info/names/environment/schema-v10/environment GETs, requires matching current module wire identities and returns no credentials or raw headers. SQL/WS hooks are not an observer substitute. Host callers retain control serialization and establish actual runtime ownership. The module wire identity is native Keccak-256 represented through little-endian u256, distinct from Wasm SHA-256 and native digest display.

Initialized startup verification must recover current persisted `st_module` in a private stopped-source copy with external bootstrap `program-bytes` inaccessible. Historical control plus missing replicas can otherwise reconstruct matching initial metadata and empty data. The original stays stopped and unchanged; verifier effects stay contained. Host/store ownership, process/job/lifetime absence, source seals, fences, epoch and startup serialization construct the opaque startup proof. The P09-02 manual experiment does not construct that production proof or enable live starts. Exact data coverage also requires independently qualified native framing/high-water and replay evidence, not metadata equality or native `Complete` alone. See [the pinned runtime contract](evidence/p09-runtime.md).

Shared startup proof binds explicit engine, incarnation, database, binding and epoch IDs, local destination ownership, settled source state and an affirmative unfenced/released state. The SQLite runtime then independently verifies the exact Litestream configuration and hash. A common identity proof is not an engine-service permit by itself. Missing/unknown engine, fence or ownership and cross-engine evidence refuse startup. Existing durable-fence, credential, reboot and archive rules remain in force.

Persistent allocated/unknown desired and plan goldens, a historical SQLite restore-receipt golden, engine-tag refusal tests and store/permit tests establish this local boundary. No physical SpacetimeDB behavior is claimed. D13 and Phase 09 require routine online native snapshot/log archival with existing encrypted tooling and separately fenced exact data operations.

### Native capture structural prerequisites (P09-05)

`internal/nativecapture` owns bounded records under `brine-native-capture-v1` for
SpacetimeDB `2.11.0`, upstream commit
`6b6e0ab19e4f8f52472aa4379552e9593c8f53bb`. Constructors validate supplied
records. They do not establish source ownership, replay success, or operation
authority.

`Inventory` owns private member records and rejects duplicate or unsafe paths.
Native log names contain a 20-digit offset. Finalized snapshot paths contain the
same offset in their directory and manifest names. Object paths use exactly two
lowercase hexadecimal digits followed by 62 lowercase hexadecimal digits.
`CheckUnchanged` compares every supplied member's content and filesystem facts.
Detection of a removed complete tail requires an independently sealed whole-source
inventory, not just the selected backup members.

`Selection` has closed `snapshot` and `logs` variants. The snapshot choice uses
the latest finalized manifest with a containing or immediately adjacent log
suffix. A snapshot can lie inside the only segment or have an empty suffix.
The log-only choice requires a segment starting at offset zero. `TailMember`
describes the last selected filename, not a committed high-water. Qualified
native framing and replay must still prove snapshot eligibility, suffix
continuity, object completeness, and recovered state.

`Checkpoint` owns canonical control, module, schema, configuration, and signing
recovery records. Its binding, epoch, and replica scope allows ordinary captures
to reuse one unchanged control generation. `Source` has closed unpublished and
initialized variants. Unpublished records omit database, publisher owner,
module, replica, and snapshot facts. Initialized records bind the checkpoint to
the selected member records and reject crossed scopes or unselected members.
All constructors and getters own mutable data. Codecs reject unknown, missing,
duplicate, null, and crossed fields. Combined source limits count the encoded
manifest, checkpoint members, and native members.

Signing recovery records contain only immutable references, versions, and
hashes. Recovery uses either an encrypted archive member or a tested external
secret version. Tokens and the archive recovery password are not members.
This unit supplies no filesystem bridge, uploader, native reader, receipt,
fence, startup permission, or public dispatch. Real contained replay, managed
ownership, credentials, jobs, and remote storage acceptance remain required.

### Candidate schema and publication records (P09-06 core)

`spacetimedb.DecodeDescriptorV10` requires exactly one `V10` envelope and delegates body validation to `CanonicalSchemaV10`. It accepts metadata, not proof that a Wasm artifact produced it. Pinned native `extract-schema` executes guest initialization and description code; protected extraction must contain those effects and establish the artifact/schema binding. The six captured native descriptors and their content pins are retained with [the core evidence](evidence/p09-schema-publication.md).

`CompareSchemas` returns immutable changes and required checks. Every update, including equal schemas with changed code, requires guest review and preserved-data verification. New tables require independent new-table data evidence. Appended U8 defaults require old-column projection and default-value evidence. Removal, rename, reordering, type/access changes and changes to existing keys, indexes, constraints, sequences or defaults require a separate operator procedure. Unqualified appended defaults refuse. Callable, lifecycle and scheduler changes add explicit effects qualification. No result says an update is safe to apply.

`datapublish.Plan` binds requester, current target/policy/desired/generation, app/incarnation/database/binding/epoch, source point/fence/preparation owner, candidate Wasm/native-program/schema, review and recovery releases, native preflight references and a bounded UTC expiry. Its closed source arms distinguish a recovered empty owned server bundle from initialized captured history. The empty arm has no native database, module, replica or history placeholders. Captured history is restricted to the fixed supported artifact/schema pairs under `brine-recovery-pairs-confirmed-v1`; a supplied contract label cannot widen it. Requirements derive from opaque canonical schemas during construction and private decoding. Secret references must belong to the app and be distinct across roles. Operator-selected key names and equal numeric versions across different roles are valid. Protected host lookups must independently bind those exact references to their required roles before effects.

The private cursor separates immutable preparation ownership from the apply operation and consumer claim. It sequences restore, preflight, publication, data verification, disposal and an external final-transition receipt. Unknown outcomes permit inspection and matching observation only. A proven unperformed publication still consumes that attempt. `DecodeCursor` requires an exact record hash loaded independently from protected retained state. Computing the expected hash from supplied cursor bytes does not prevent rewinding history. Store composition must atomically retain each record/hash and compare-and-swap its head, authenticate proof references and enforce one consumer per point/plan.

These records grant no mutation, swap, epoch activation or fence-release authority. Protected extraction, exact-point consumption, staged data/projection/effects evidence, store/jobs/host wiring and physical acceptance remain required. P09-06 and T39 remain unchecked.

### Definition and typed identities

~~~toml
[runtime]
uid = 10001
gid = 10001

[[databases]]
name = "main"
persistent_root = "/srv/brine-data"
mount_path = "/data"
filename = "app.db"
backup_destination = "primary"
sync_interval = "1m"
~~~

Per-database `sync_interval` is a positive Go duration string, default `1m`. Operator `[backup]` sets `min_sync_interval` (default `10s`) and `max_sync_interval` (default `1h`). A spec interval outside these inclusive bounds produces a clear `backup_cadence_denied` plan conflict, not a parse or policy rejection. The operator separately sets `snapshot_interval` (default `6h`), bounded by `min_snapshot_interval` (default `1h`) and `max_snapshot_interval` (default `24h`). Invalid or unordered operator bounds refuse policy parsing. Canonical desired state and plans carry the effective durations for the replica renderer. Litestream `monitor-interval: 1s` and `checkpoint-interval: 1m` remain fixed, not configurable. The approximate loss window is `sync_interval` plus upload time, not a durability promise; during upload failure the recovery-point bound is unknown.

An admitted sync/snapshot-only change is a `revise_replica` lifecycle. It preserves the application unit, route, port, image, secrets and replica epoch. Each exact config is published at its content-addressed immutable path; the stable control socket and lifetime-lock inode do not change. A durable per-binding cursor records pending storage, config publication, stop intent, observed stop, binding commitment, start intent and observed activation. Service replacement and the append-only binding revision commit run under the lifetime lock, and fresh replica permits gate activation. Unknown stop/start attempts are inspected, never reissued. A held fence permits only storing the pending revision: no config publication, binding activation or replica/app restart occurs until safe resume. Successful activation commits only release metadata so an unchanged cadence is subsequently a no-op. This is activation evidence, not remote durability evidence.


`spec.App` gains `Runtime *RuntimeIdentity` and `Databases []Database`; `RuntimeIdentity` holds `UID uint32` and `GID uint32`. `Database` holds typed `DatabaseName`, `PersistentRoot`, `ContainerMountPath`, `DatabaseFilename` and `BackupDestinationRef`. Runtime is required for databases, UID/GID are 1..65535, and every database writer uses those identities. Database names/destination references are 1..63 lowercase ASCII letters/digits/hyphens, starting with a letter. Filenames are one 1..128-byte ASCII basename, start with a letter/digit, and allow letters/digits/dot/underscore/hyphen, except `.` / `..` and names ending in `-wal`, `-shm`, `-journal` or `-litestream`. No slashes, interpolation, control characters or symlinks. At most 16 databases; names are unique. Mount paths are clean absolute POSIX directories; refuse root, overlapping mounts and system/secret/runtime paths (`/proc`, `/sys`, `/dev`, `/run`, `/etc` and their descendants). `persistent_root` exactly matches an authorized policy entry, not an arbitrary descendant. Database filename/mount identity changes on an existing database refuse pending an explicit data move design. Migrations are separate P04-07 plans, never deploy side effects.

`policy.Desired` carries the normalized runtime and databases in canonical bytes and `DesiredHash`. Add `backup_destinations` to root-owned policy as typed records containing `reference`, HTTPS `endpoint`, explicit `region`, `bucket`, `base_prefix`, `path_style`, `credential_ref`, and protected lifecycle-verification identity/freshness. No credential value or parent API token is policy/state. Plans bind the destination record/hash, selected root and quota observation. Refuse unknown destinations and unverified filesystem/mapping/retention facts.

`internal/data` owns validated identities/layout and containment. `AppIncarnationID`, `DatabaseID`, `ReplicaBindingID`, `ReplicaEpochID` and `ArchiveID` are independent immutable random 128-bit IDs encoded as 32 lowercase hex characters, allocated once in the store under the host mutation lock and reused during reconciliation. They are not app names or release IDs. `DatabaseBinding` records database ID/name, incarnation, root, relative directory, mount path, filename and replica binding. `ReplicaBinding` records binding/database/destination IDs, epoch, exact remote prefix, credential version and unit/config hashes. `ArchiveRecord` records archive/incarnation IDs, original and archived relative paths, all replica epochs, removal operation, commit timestamp and retention deadline. Store uniqueness prevents live sharing of paths/destinations; archive references survive removal. Secret receipts contain references/versions/expiry only.

### Paths and units

Let `P` be the verified policy root and `S` the existing runner Brine state directory. No user request supplies the derived suffixes.

| Artifact | Fixed derived path/name |
| --- | --- |
| Live app tree | `P/apps/<incarnation-id>/` |
| Database mount source | `P/apps/<incarnation-id>/databases/<database-id>/` |
| DB and SQLite siblings | `<source>/<filename>`, `<filename>-wal`, `<filename>-shm` |
| Litestream metadata | `<source>/.<filename>-litestream/` |
| Archive tree | `P/archives/<archive-id>/` (whole former app tree) |
| Isolated restore | `P/restore-tests/<operation-id>/<database-id>/<filename>` |
| Remote prefix | `<base-prefix>/apps/<incarnation-id>/databases/<database-id>/epochs/<epoch-id>/` |
| Replica config | `S/replication/<binding-id>/configs/<config-sha256>.yml` |
| Private replica control socket | `S/replication/<binding-id>/control.sock` |
| Credential file | `S/credentials/s3/<credential-ref>/v<version>.env` |
| Lifetime lock | `S/replica-locks/<binding-id>.lock` (never unlinked) |
| App Quadlet / service / container | Existing `<app>.container` / `<app>.service` / `systemd-<app>` |
| Replica unit | `~/.config/systemd/user/brine-litestream-<binding-id>.service` |
| Executable | `/opt/brine/litestream/0.5.17/litestream` |

Directories are runner-owned 0700, files 0600, with non-symlink ownership-checked ancestors. Root selection verifies that live/archive/restore trees are on the same local filesystem; archives use journaled rename plus parent fsync, not copy-and-delete. Record/verify device and inode identities at effect boundaries. Reject state/release/credential roots and roots overlapping another policy root. Mount only `<source>:<mount_path>:rw`; never `:U`. Render explicit keep-id/User/Group and `--umask=0077`. SELinux requires approved shared `:z` labeling and verified host access; otherwise refuse. No rootless hard disk quota is promised. Report bytes/inodes, any observed operator-enforced quota, and minimum-free-space admission.

The replica unit uses `EnvironmentFile=<credential-file>`, `UMask=0077`, and fixed `brine host replica-exec <database-id> <binding-id> <epoch-id> <config-hash> <config-path> <credential-path>`. This command acquires the nonblocking lifetime kernel flock itself, repeats the read-only permit check after acquisition, securely reads the immutable credential file, and replaces itself with `/opt/brine/litestream/0.5.17/litestream replicate -config <config>` using a fresh allowlisted environment. It clears `FD_CLOEXEC` on only the lifetime-lock descriptor, so the exec'd Litestream process holds the lock until exit; all other descriptors remain close-on-exec. This is equivalent to typed `/usr/bin/flock --exclusive --nonblock <lifetime-lock>` execution, but direct calls cannot bypass the lock. It installs as an ordinary user service with `WantedBy=default.target`, has no credential/debug values in unit text, and has no app-coupled `PartOf`/`BindsTo`. App `Wants`/`After` reference the replica **service** name. Every replica-process start, including boot, automatic service restart and credential rotation, executes a fixed fail-closed permit check against durable quiescence state and the committed database/binding/epoch identities. A held fence blocks both application writers and Litestream. Missing, unreadable or inconsistent permit state refuses startup. Reconciliation does not release the fence or permit replication until the source bundle, replica configuration and intended epoch are settled. Live restore cannot restart replication against the pre-restore epoch. The generated unit invokes fixed `brine host replica-permit <database-id> <binding-id> <epoch-id> <config-hash>` before execution; the read-only internal check validates durable identity/config records without recursively taking the host mutation lock. Credential replacement cannot bypass it. Replica bindings and their control state must not be activated concurrently on multiple hosts. Host-local locks do not provide cross-host exclusion. A detected foreign owner or ambiguous destination ownership refuses replication. The config has exactly one `dbs` entry and singular structured `replica` with `type: s3`, bucket/path/HTTPS endpoint, explicit destination region and path-style setting; omit credential fields and URL shorthand so temporary session tokens use the SDK provider. Before activation, Brine strictly validates serialized config against its pinned typed model, rejecting unknown/duplicate YAML keys, malformed or nonpositive durations, unsupported fields/topology, config interpolation and `l0-retention: 0`. Exactly one DB, replica and private control socket must match the committed binding/epoch; raw app config is not accepted. Upstream permissive `yaml.Unmarshal` is not an admission check. Add typo, duplicate-key, zero-retention, missing-field and config/binding-mismatch tests. Set `retention.enabled: false`; destination lifecycle evidence must exclude object expiration. `litestream status` is explicitly local-only evidence. Neither it nor unit active proves remote durability, working credentials or a restorable point. Replica environment starts from a controlled allowlist with EC2 metadata disabled, no inherited AWS profiles/providers, and no SDK debug logging. Missing credentials refuse rather than search other stores.

The default is one operator-provisioned bucket with immutable per-database/epoch prefixes. Those prefixes identify data; provider-enforced external credential restrictions determine access. Per-app/destination buckets are optional when bucket isolation is needed. Brine does not create buckets or mint credentials and contains no provider-specific credential API.

`brine backup credentials set APP` plans a `backup_credentials_set` operation. `CredentialDelivery` carries the credential-plan ID, target/binding/destination/epoch references, policy hash, externally obtained access key/secret/optional session token and optional UTC expiry over bounded private dispatcher stdin. It is never persisted as a plan/event, echoed or logged. Host authorization rechecks scope references/policy/epoch, stores the private versioned credential file and a `CredentialReceipt` containing immutable version, received time and optional supplied expiry, and refuses stale delivery. Initial binding allocation precedes credential assignment; archive access binds archive/epoch references rather than an app-name lookup. No parent/provider management token or mint helper enters Brine.

Versioned runner-owned 0600 environment files contain `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and optional `AWS_SESSION_TOKEN`. Installing a new version changes/restarts only the replica service, retaining its database, binding and epoch. Serialize activation through its lifetime lock, verify remote access and reconcile unknown outcomes. No app restart occurs. Backup/restore admission checks supplied remaining lifetime; status reports age, expiry/renewal-needed and authentication/network failures. An external operator/script/layer renews credentials. Archive restores may require newly supplied credentials for their authorized destination. Provider scope tests prove least privilege; Brine's prefix records alone cannot prove a key's scope.

### State transitions and restore evidence

The native replacement prerequisite adds append-only epoch reservations and an
intent-first bundle transition journal. Its latest immutable runtime commit is
the sole active head. Activation compares the expected old version and commits
the new runtime, per-epoch allocation/checkpoint references and activation event
in one transaction while the original preparation fence stays held. Native
permits resolve that committed epoch and incoming inode; original binding and
allocation bytes remain historical. Every retained prefix enters destination
ownership checks, and a new epoch cannot reuse an earlier prefix even for the
same binding. Generic fence release refuses a nonterminal transition.

First publication binds an affirmative unpublished runtime head and its protected
allocation record. That head contains owned configuration and resource limits;
it does not establish an initialized database. The store requires its exact
version/hash, unpublished state, empty database/publisher identities and matching
allocation identity/hash. Initialized replacement requires an initialized head.
Both arms activate the next version of their recorded head. Missing runtime
history never implies an unpublished source. Launch and lifetime capabilities
must match the protected state directory, binding, role and current lock inode.

The protected point/publication callers still own exact preparation, the
immutable one-consumer join, target verification and fresh completion authority.
Structural transition records and private storage helpers do not provide those
proofs or admit public publish/live-restore operations. Normal managed start is
separate from data completion. The rooted movement helper preserves the old
binding leaf and promotes only the recorded staged leaf with pinned no-follow
parents, same-device identity, no-replace rename and parent fsync. An uncertain
rename is inspected; an already moved recorded inode permits sync and adoption
only, never a second rename or an automatic data rewind.

Deploy/update/rollback preserve incarnation/data/binding and may leave replication running. Removal/live restore require independent app-writer and replica-process/lock quiescence, not `systemctl stop` exit alone. Before freeze, verify a fresh remote restore point covering the recorded marker/TXID. Unknown upload/stop/swap outcomes enter reconciliation. No retry starts a competing writer. Archive retention begins at committed removal, lasts 30 days and is keyed only by archive ID. Recreation never reuses its incarnation or replica prefix. Purge/expiry recheck all live path and destination references immediately before deleting local archived data. They never delete remote backup objects or prefixes; Brine has no provider-delete authority. Local metadata and all recorded remote epochs stay bound to that archive.

An archive must distinguish `never_written` from initialized data. The former requires exact allocation device/inode evidence, a fresh affirmative empty observation, no application writer history and settled initialization attempts. It retains that evidence rather than claiming a remote restore receipt. Its local data retention also lasts 30 days from committed retirement; ID-only early purge still requires its own explicit reviewed plan and apply under the single enrolled identity, not an operator/agent role flag. Initialized or unknown data cannot use this exception. Initialized data needs the independently verified fresh remote restore point and restorable backup retention described above; unknown data refuses. These archive and expiry rules are execution requirements for P04-08, not completed removal behavior.

The enrolled archive preparation command is `brine host archive-prepare APP --plan`, followed by `brine host archive-prepare APP --plan-id PLAN_ID`. This is synchronous preparation, not detached removal and not a remote dispatcher operation. Its immutable plan records an archive ID, the active incarnation and optional release, allocation and root/source device/inode identities, ID-derived source/archive paths, every recorded replica epoch and destination, and schema/invariant declarations. Policy, target identity and inventory generation are rechecked before claiming an attempt. Unsettled initialization, unknown ownership, replaced directories and held fences refuse.

Archive planning uses the same identity-bound filesystem evidence as persistent inventory: fresh read-only root inspection supplies ownership, filesystem, capacity and device/inode identity; the retained allocation receipt supplies affirmative POSIX-lock and durable-rename probe results from approved data preparation. Both must name the exact same root, device, inode and filesystem. Missing or incomplete probes, substituted roots and changed filesystems refuse. Planning does not rerun mutating probes or invent capability flags from inspection alone.

Preparation atomically claims one attempt per incarnation and durable per-database fences before stopping managed writers. Independent systemd PID/cgroup/job and Podman process/mount observations must affirm writer quiescence. Initialized data gets an exact upload barrier and independent isolated remote restore, or, for a never-published replica, a bounded read-only SQLite snapshot uploaded create-only and independently restored. The receipt binds the quiesced-writer cutoff to that exact remote source and verifies schema, integrity, foreign keys and declared invariants. Never-written allocations instead require fresh affirmative emptiness and unchanged allocation identities. All recorded destinations require fresh operator no-object-expiration evidence. Initialized preparation currently refuses multiple recorded epochs at planning and rechecks that support before claiming fences or stopping writers; this does not discard them from the archive plan.

Only after coverage is journaled does preparation stop and independently inspect replicas, acquire their lifetime kernel locks, and recheck writers and allocation/tree identities. It then atomically stores an immutable manifest and terminal preparation event. Intent, writer stop, coverage and replica stop have separate append-only journal boundaries. Locks are released on return, but durable fences remain held, so managed writer/replica startup stays blocked. A retained prepared manifest is historical evidence, not permission to rename without new quiescence checks. Repeating the same completed preparation reads the manifest without replaying effects. Any incomplete attempt refuses replay with `recovery_required`; pre-manifest abandonment is available below, while preparation adoption/reconciliation is not yet implemented.

Preparation itself never renames data or retires ownership. An archive attempt has two enrolled-identity exits, both addressed by immutable archive ID:

- `brine host archive-retire ARCHIVE_ID` freshly rechecks ownership, coverage, retention admission, independent writer/replica quiescence and replica lifetime locks. It journals owned route/unit cleanup, then each same-filesystem tree rename using no-replacement semantics and directory fsync. Persistent unit removal verifies exact owned rendered bytes and never touches mounted data. All cleanup prerequisites pass before intent. Rename pins private no-follow source/destination parent handles and passes only ID leaf names to `renameat2`; parent swaps cannot redirect it outside the recorded root. Exact tree/allocation identities are checked before and after movement. The final transaction retires the active incarnation and release head, retaining immutable manifests, epoch records and held old-incarnation fences. Recreation through normal reservation uses fresh incarnation, database, binding, epoch, paths and remote-prefix identities. A committed receipt records thirty days from retirement; ongoing restorable retention and credential renewal are not yet implemented.
- `brine host archive-abandon ARCHIVE_ID` freshly verifies exact ownership, source/allocation identities, schema and independent quiescence under replica lifetime locks, then atomically releases only that operation’s fences. It resolves the immutable plan without requiring a completed manifest, supports every pre-manifest preparation stage and adopts its own interrupted abandonment intent. It journals abandonment before stopping any owned writer or replica; unknown or unowned services refuse. Stale backup evidence or unrelated inventory/policy changes do not strand abandonment; current roots/destinations must still be authorized. It leaves data in place and restores normal managed startup permission, but does not start services. Resume a committed app through the ordinary start/apply workflow; a prepared-only app continues through ordinary initialization/deployment. A later archive attempt receives new archive/operation identities.

Completed matching dispositions return their retained receipts without repeating effects. Interrupted cleanup and rename still refuse ordinary execution replay with `recovery_required`. `brine host archive-reconcile ARCHIVE_ID --inspect` reads their retained journal, exact runtime artifact ownership and source/archive inode locations without changing runtime or data. Its `can_reconcile` field describes journal/artifact structure, not fresh quiescence or remote admission. `brine host archive-reconcile ARCHIVE_ID` settles only that recorded attempt under the host lock and replica lifetime locks. It independently verifies manager/process/mount quiescence at both recorded path locations, exact held fences and current incarnation/database/epoch ownership, current authorized roots/destinations and fresh retention admission. Initialized data is independently restored again from the original exact remote point; expired preparation evidence is not relabeled fresh. Current schema must still match that coverage. Never-written directories must still be affirmatively empty. Partial runtime cleanup is verified before remaining owned artifacts are removed; an uncertain route withdrawal is validated and reloaded through Caddy settlement. A rename intent admits source-only movement or archive-only adoption of the recorded inode after pinned-parent fsync, never a conflicting, missing, replaced or out-of-order tree. Final retirement repeats exact ownership checks inside its transaction. Recovery never starts or stops a service and never clears a fence. Terminal receipts are historical and do not inspect a recreated app. A cleanup intent may exit through abandonment only after fresh verification proves every recorded unit, replica artifact, route and source/allocation identity remains pristine. Any actual partial cleanup or movement is non-abandonable. Interrupted abandonment can resume only after repeating fresh ownership/schema/quiescence verification and acquiring lifetime locks. Use only an explicitly authorized disposable fixture until retention enforcement and physical evidence ship. The normal persistent removal command continues to refuse. ID-only expiry/purge remains unimplemented; these commands never delete remote objects. Local unit/fake-storage evidence is not physical archive or provider evidence.

`restore test` resolves a stored remote binding/epoch and renders a private structured config. Invoke typed `litestream restore -config <restore-config> -o <fresh-output> <config-db-path>`; the final DB path is a config selector, not a read of live DB bytes. Never omit `-o`, use `-force`, mount live data in the fixture, or substitute local metadata. Require new output and no WAL/SHM/journal siblings. Run `PRAGMA integrity_check`, require `PRAGMA foreign_key_check` to return no rows, and check fixture marker/invariants through a read-only checker; no application-supplied SQL. `RestoreTestResult` holds operation/database/binding/epoch IDs, source identity, tool version, requested restore point, recovered TXID/marker/commit time when known, observed time, integrity/invariant outcomes and an explicit known/unknown recovery-point bound. Generic production databases without a supported invariant return that check as unknown, not passed. No public listener is created.

Live restore separately journals its safety restore point, quiescence, verified isolated copy, original bundle preservation and swap. It creates a new epoch with empty local metadata before replication resumes; the pre-restore epoch remains recorded for recovery. Application rollback never rewinds data.
### Schema evidence and fenced backup preparation

P04-01 adds `SchemaDefinition{Database, Marker, CatalogSHA256}` entries beside D12's compatibility declarations. Each accepted marker resolves to exactly one immutable definition; marker remapping in an incarnation refuses. `SchemaObservation` is a tagged union of `allocated_empty`, `verified_empty`, `verified_schema` and `unknown`, with database identity, observed time, marker/catalog fingerprint when known and fixed unknown reason. Reserved `brine-empty-v1` represents affirmatively empty data, not a missing-file fallback.

The observer opens only an ownership/containment-verified existing file using SQLite `mode=ro`, including its verified WAL/SHM siblings, and reads integrity/schema in one consistent transaction. The fixed `brine_schema_marker` table has columns `id INTEGER PRIMARY KEY CHECK(id=1)`, `marker TEXT NOT NULL`, `catalog_sha256 TEXT NOT NULL`, exactly one row with `id=1`, and no other columns/triggers. Validate its structure before trusting the row. Compute SHA-256 of the normative catalog serialization below, ordered by binary `(type,name)`. Exclude `sqlite_*`, the verified marker table and `_litestream_seq`/`_litestream_lock` only when their structures match pinned v0.5.17 definitions. Do not include rootpage. Measured hash must equal both marker-row hash and declared immutable definition. Corruption, missing/nonmatching definitions/markers or malformed internal tables are unknown. An empty valid database has no application objects/marker table. Missing DB is empty only with a fresh untouched allocation receipt and no siblings/history; otherwise missing, zero-byte, inaccessible or unreadable data is unknown. SQLite `mode=ro` is logically read-only but may touch SHM/locking bookkeeping; it does not initialize a database. Never use `immutable=1` to read live WAL data. Acceptance cases include absent SHM, nonwritable SHM and concurrent WAL commits: verify a consistent observed transaction where SQLite can safely read it, otherwise return unknown without creating a missing DB, broadening permissions or skipping WAL. Existing owned SHM bookkeeping is permitted, not an ownership/permission repair. P04-01 carries evidence/declarations through canonical planning and enforces D12 refusals; all start/compensation/reconcile paths freshly observe.

The normative catalog bytes are Go `encoding/json.Marshal` of a non-nil `[][4]any` row array with the four fields above. Names/types/SQL strings are exact SQLite UTF-8 strings, not normalized SQL; SQL NULL is JSON `null`. Preserve Go's default HTML escaping and U+2028/U+2029 escaping. No indentation, trailing newline, maps or alternate JSON canonicalizer. Sorting is bytewise `(type,name)` after exclusions. Golden acceptance vectors include these exact bytes and SHA-256 values:

| Catalog bytes | SHA-256 |
| --- | --- |
| `[]` | `4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945` |
| `[["table","t","t","CREATE TABLE t(x TEXT)"]]` | `688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134` |
| `[["table","t","t","CREATE TABLE t(x TEXT CHECK(x \u003c 'z'))"]]` | `ba63ad9fe787183ed1f090284ea94e14eded8dd5cdaa8803ba4d66d79abdc559` |

Golden tests also cover SQL NULL, Unicode, U+2028/U+2029, HTML escaping, row ordering and excluded-table structure validation. The reserved empty marker uses the empty-array digest.

`QuiescenceFence` records immutable fence ID, incarnation/database IDs, preparing operation and state `held` or `released`. Under the host mutation lock, `prepare_restore_point` stores it before any stop. All Brine application and replica start paths refuse a held fence. Every replica-process start, including boot, automatic service restart and credential rotation, executes a fixed fail-closed permit check against durable quiescence state and the committed database/binding/epoch identities. A held fence blocks both application writers and Litestream. Missing, unreadable or inconsistent permit state refuses startup. Reconciliation does not release the fence or permit replication until the source bundle, replica configuration and intended epoch are settled. Live restore cannot restart replication against the pre-restore epoch. The generated app unit executes a fixed `brine host writer-permit <incarnation-id>` condition before starting; that internal read-only check reads durable fence state and needs no recursive host-lock acquisition. No user-supplied command, automatic expiry or restart clears the fence. Unknown stops remain fenced and require reconciliation. A separate planned resume checks current release/schema compatibility before release. Migration/restore plans bind the held fence; replanning and client loss do not restart the app.

`PrepareRestorePointIntent` binds incarnation/databases, fence, authorized destinations and explicit freshness/recovery-window bounds, not arbitrary paths/SQL. Preparation stops/inspects app writers and replica services/processes/lifetime locks, then uses SQLite's backup API with a read-only source to create a private standalone snapshot incorporating committed WAL. For fresh `allocated_empty`, create a valid empty staging SQLite file only and retain the original absence proof. Compute snapshot byte SHA-256/size and schema observation after integrity checks; upload create-only to `<epoch-prefix>/restore-points/<point-id>/snapshot.sqlite`. Atomic create-only upload is a demonstrated admission capability, for example `If-None-Match: *`; unsupported/unknown capability refuses preparation. Tests race two writes to the same object and require exactly one winner with unchanged winning bytes. Never fall back to HEAD then PUT. Fetch that exact object into a fresh isolated output, verify byte hash/size, integrity and expected schema/invariants, and record its successful receipt while writers stay fenced. Never infer an empty point from nonexistent replication history.

`RestoreSource` is tagged `litestream_ltx` with exact binding/epoch and selected LTX restore point, or `sqlite_snapshot` with point ID, exact immutable object key, SHA-256 and size. Snapshot tests use bounded typed S3 download and independent checks; LTX tests use the pinned CLI/config. An exact LTX point first executes bounded `litestream sync -wait -json -socket <binding-control-socket> <db-path>` while app writers remain quiesced and the already-running replica finishes uploading. Its receipt records both `txid` and `replica_txid`, requiring `replica_txid >= txid`; failure/timeout is unknown. Restore the selected TXID explicitly with `-txid <hex-txid>` and bind the successful isolated verification to that exact point before stopping the replica. A held fence blocks any new replica start, not the already-running upload barrier. Never substitute local status or a timestamp for this receipt. Timestamp restoration selects retained LTX file boundaries and is not per-transaction recovery. Both read S3-compatible storage, neither reads a live DB. `VerifiedRestorePoint` binds source, database/empty proof, fence, source schema, verification receipt/times and retention/freshness bounds. It grants no migration authority. New migration/live-restore plans bind it without silently swapping receipts. Before mutation, apply verifies fence ownership, actual quiescence and unchanged source file/WAL identities/schema; uncertainty requires a new point and plan. Retain the original source and point through the recorded recovery window. Reconcile never reruns preparation's upload or resumes a writer solely because a receipt exists. Snapshot and LTX verification both require integrity, foreign-key and declared invariant/schema checks. Reboot/interruption tests stop at replica stop, archive rename, DB swap and new-epoch activation, then assert neither app nor replica starts while fenced, old config/epoch never activates after live swap, and unknown permit state fails closed. Real S3-compatible storage tests must verify both source tags; the ordinary Litestream gate remains mandatory.

## Schema migrations

**Approved under D12.** Release compatibility checks and reviewed empty-data initialization are implemented. General schema migration execution remains a design target. This contract preserves D11's data layout, mount lifecycle and replication guarantees. It applies to app databases, not control-database upgrades.

### Client schema files

A SQLite database may declare `schema = "db/main.sql"` and an optional `marker`
under `[[databases]]`. The clean relative path is resolved against `brine.toml`
on the client only, using a directory-confined open that refuses symlink files
and directory components. The file contains at most 128 semicolon-terminated
bounded DDL entries and at most 1 MiB. The client and host share one in-memory
compiler and catalog observer. Comments and inter-token whitespace are
canonicalized; quoted values remain significant. Blob prefixes must be adjacent
to their literal. If normalization changes SQLite's inferred view column names,
the original view statement is retained. Explicit view column names or `AS`
aliases avoid formatting-dependent inferred names. Refusals identify the source
line.

The client derives the catalog SHA-256, initializer bytes and exact artifact
SHA-256. The default marker is `sha256:<first 12 lowercase catalog hex digits>`.
An explicit marker must be valid and cannot name the empty schema. Handwritten
`schema_definitions` and `catalog_sha256` are refused. Internal retained schema
definitions remain immutable catalog identities, not author-facing spec fields.

Connected plan requests contain the original spec and required
`schema_artifacts` metadata (an empty/null list for no schema files). Every
artifact has exactly `definition` (database, marker, catalog SHA-256) and
`digest`. No SQL or artifact bytes cross the planning boundary. The host checks
metadata against declared databases and markers, while the separate initialization plan carries exact reviewed bytes and the host
independently recompiles them before live-file creation. The unprivileged host
publishes a digest-bound artifact only after claiming initialization intent and fence. Metadata alone grants no data authority.
Plans show derived artifact digests. `brine data schema <brine.toml> --database
NAME` exports the exact canonical JSON bytes without a trailing newline. Machine modes use the standard envelope with `data.artifact` metadata and the exact JSON in the `data.bytes` string.

### Release compatibility declaration

The app spec accepts a `schema_compatibility` array with one entry per declared persistent database. The entry has exactly these fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `database` | validated database reference | Refers to the app's P04-01 database declaration, never a host path or backup destination. |
| `accepts` | nonempty set of bounded schema markers | Exact schemas this release can read and write without a schema change. No wildcards, ranges or inferred ordering. |
| `startup` | enum with only `preserve` | Startup, restart, normal writes and health checks do not change schema. |

Markers are app-defined ASCII tokens matching `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}` or derived tokens matching `sha256:[a-f0-9]{12}`. They identify schema structure and application invariants, not release order. The same marker cannot be reused for a different meaning within an app incarnation. Each `accepts` set has at most 128 unique markers and sorts lexically in canonical input. Entries sort by database reference. Duplicate database entries, unknown references, invalid markers, extra fields and unsupported startup modes fail strict validation. For a database with a schema file, a missing entry defaults to accepting that file's current marker with `startup = "preserve"`. Without a schema file, missing entries remain a planning conflict.

For example, a release that can use either schema `orders-v1` or `orders-v2` declares both in `accepts`. A release that can use only `orders-v2` cannot deploy against `orders-v1`, and an `orders-v1`-only release cannot roll back against `orders-v2`. Brine does not infer that adding a column is safe or that a higher version contains a lower one.

The desired input retains the declaration alongside the pinned index and platform manifest digests. Release history retains that exact input. Planning binds each database reference to its immutable data identity and measured current schema marker, separately from the current app release. A migration or live restore can change the current schema without changing the app release. A previous release's marker, a backup's marker or a client assertion cannot substitute for current schema evidence. Unknown or contradictory observations stay unknown.

A persistent deploy, including creation and config updates that can restart a writer, must preserve every current schema marker and show that the candidate accepts it. Start and restart check the committed release's declaration against current schema before starting a writer. Stop remains available without a compatibility declaration. Removing a declaration or data reference cannot reclassify existing persistent state as stateless. Newly allocated empty data needs a recorded empty-schema marker. Creating app tables is also a schema change and requires the separate reviewed workflow, not an exception for first deployment.

Deployment plans contain no schema-change payload, migration hook, SQL text or arbitrary shell command. Images must have implicit startup migrations disabled before the release declares `preserve`. Brine enforces declared intent and recorded schema checks, not a sandbox against trusted application code that can already write its own database. A false declaration cannot make a hidden migration safe.

### Refusals and next steps

These codes are future plan conflict diagnostics, not additions to today's top-level error envelope. A completed planning analysis returns `ok: true`, exit 0, `kind: conflict` and no changes, matching the existing planner contract. Callers inspect `kind`. Attempting to apply a conflict or a plan with changed schema evidence refuses before effects in exit category 5.

| Conflict code | Field | Fixed explanation and next step |
| --- | --- | --- |
| `schema_compatibility_required` | `schema_compatibility` | Persistent releases must declare every database's accepted schemas and schema-preserving startup. Review the pinned image and add the declaration before replanning. |
| `schema_state_unknown` | `schema_state` | Current database schema is unverified. Establish authoritative schema evidence before replanning. Do not guess from release history. |
| `schema_incompatible` | `schema_compatibility` | This release cannot use the current database schema without changing it. Select a compatible release or prepare a separate reviewed migration. |
| `schema_rollback_incompatible` | `schema_compatibility` | The rollback release cannot use the current database schema. App rollback cannot restore data. Select compatible code or use the separately authorized live restore procedure. |
| `migration_plan_required` | `migration` | Deployment cannot request a schema change. Prepare a separate migration plan with reviewed artifacts, restore evidence and rollback semantics. |
| `migration_restore_point_required` | `restore_point` | Every affected database needs a fresh verified recoverable point. Refresh backup status and complete an isolated restore test before making a new migration plan. |
| `migration_rollback_incompatible` | `rollback_policy` | The resulting schema cannot support the recorded current and rollback releases. Routine migration automation refuses. Use a reviewed operator procedure with compatible forward recovery code. |

Malformed migration fields remain validation errors. A well-formed schema-change request in a future deploy input produces `migration_plan_required`, never executable changes. Current schema, database identity, compatibility sets and migration receipts enter canonical decision facts. Changes require a new immutable plan. Clock-based backup age checks happen in the connected service and apply gate, not inside the pure planner.

### Reviewed initialization artifact delivery

Initialization planning accepts exact `app`, `first_release_plan`, `artifact` (SHA-256) and `artifact_bytes` (JSON base64) fields. The artifact is strict initializer JSON, bounded to 1 MiB. Apply accepts only `app` and `plan_id`. The host compiles the bytes with the shared SQLite compiler; the immutable initialization plan includes the actual catalog and binds its fingerprint, artifact digest, bytes, requester and recovery bounds. Human review displays SQL and catalog; machine output retains both. Ordinary deployment/preparation remains metadata-only.

The restricted-channel personal identity may plan and apply initialization, with no agent-only grant or local operator command. The runner retains review payloads with immutable plans in private control state. Only after the durable one-attempt intent and data fence are claimed does apply publish an immutable digest-addressed artifact in that state. It recompiles before publication and uses the reviewed initializer for mutation. Same-digest publication and re-apply of the same accepted job are idempotent. A tampered digest, retained review or catalog refuses before publication/mutation. Interruption after storage reconciles by inspecting actual schema; it never reruns storage, initialization or restore upload. A proven untouched/empty destination settles `not_initialized`; unknown state retains its fence. There is no root-owned schema store or privileged artifact dispatcher.

### Explicit migration plan and operation

This typed design is reserved for later automation. It introduces neither a working CLI verb nor dispatcher authorization today. A `MigrationPlan` is separate from `plan.Plan`'s deploy changes. Its immutable fields include:

- The target identity, policy version and hash, observed control generation and app incarnation. The current-release binding is tagged `committed` with the exact release ID, or `absent` only for reviewed initialization of affirmatively empty data. Initialization also binds the intended first release's exact desired input and image digests. Absence is evidence, never a default.
- A database-reference-sorted array of transitions. Each binds the immutable database identity, observed source schema marker and destination marker.
- A reviewed, digest-pinned migration image with its selected platform manifest, a bounded container-local executable and separate typed arguments. No host shell, user-supplied host executable, raw runtime flags, arbitrary mounts or SQL in deploy input.
- A content-addressed review receipt bound to the exact transitions, image, invocation and checks. Review is bound to the immutable plan applied by the same restricted-channel personal identity (D8/D12); no separate operator identity or migration grant exists. A request's `approved` boolean or `--yes` cannot replace that review.
- A restore-point record per database with the immutable backup ID, backup destination identity, source schema marker, recovered checkpoint, snapshot time, verification time and successful P04-05 restore-test receipt ID. Credentials, literal data and local restore paths are absent from public presentations and events.
- A typed validation set with SQLite integrity checks, expected destination marker and reviewed app invariant-check artifact references. Checks run through bounded adapters, not free-form host hooks.
- A tagged rollback policy. Initial automation accepts only `app_compatible`, listing the current release when present and the designated retained rollback release IDs with their verified declarations. For empty-data initialization, the intended first release must accept the destination schema. There is no old writer to restore, and failed or partial initialization requires recovery rather than data deletion. An operator-only breaking-change procedure uses `recovery_only` and records compatible forward recovery code and the live restore runbook. It is not routine automation authority.
- Operator bounds `max_backup_age_seconds`, `max_restore_test_age_seconds` and `recovery_window_seconds`, all explicit positive values. No unbounded or guessed freshness default. The restore points remain retained through the operation and at least the recovery window after completion. Absolute expiry belongs outside the canonical fingerprint, while the selected evidence and bounds remain hashed.

P04-04 must prove the chosen backup is available and covers its recorded recovery point. P04-05 must restore that exact point into an isolated nonpublic path, pass `PRAGMA integrity_check` and the declared app invariants, and verify its schema marker. The point must remain retained throughout the migration and its recorded recovery window. Merely running Litestream, reporting low lag or reusing an unrelated successful drill is insufficient.

Planning reads those receipts and records immutable intent. It does not stop an app, make a backup, mutate schema or perform a live restore. Before schema mutation, apply acquires the host lock, revalidates ownership and policy, quiesces all app writers and the affected replicators, and establishes that each verified point covers the exact pre-migration data. If unbacked writes followed the snapshot, coverage is unknown, receipts expired or a backup disappeared, apply refuses before mutation. Preparation must produce fresh verified points and a new plan. Apply cannot silently replace the bound restore point. The backup preparation workflow must support a verified point while writers remain quiesced.

An allowed `migrate_schema` operation records its plan, authenticated requester, review receipt and restore points durably before any schema change. Its journal records bounded intent, observed outcome, validation and source/destination schema evidence. It uses the host mutation lock and the existing interruption discipline. Completion records current schema independently of release heads and invalidates queued plans against the previous schema. It neither installs an app release nor rotates release heads. Writer and replicator resumption follows the recorded safe ordering and requires that the installed release accepts the resulting schema.

An uncertain invocation does not rerun a migration or its down script. Reconciliation inspects actual schema and migration receipts. Unknown, partial or failed invariant evidence keeps writers quiesced and records `recovery_required`. A backup permits a separate restore decision. It does not make non-idempotent SQL retryable or authorize an automatic database rewind.

### App rollback after a schema change

Rollback planning compares the selected immutable release's declaration with each database's current measured schema, not the schema at that release's original deployment. Compatible rollback preserves data and schema. Incompatible rollback conflicts with `schema_rollback_incompatible`. Unknown evidence conflicts with `schema_state_unknown`. Neither starts an operation or rewrites a database.

Apply revalidates that comparison under the host lock before effects. Automatic deployment compensation, reconcile and resolve also need fresh schema proof before restoring an old unit or starting its writer, even if the candidate never reached healthy or schema mutation occurred outside deployment. The proof must cover the destination writer and every database it uses. `stateless_compatible` applies only to affirmatively stateless desired and observed data state. `compatibility_verified` requires current schema evidence and the destination release's declaration. It is not a reusable approval for any later schema. Unproven compensation becomes `recovery_required` rather than `rolled_back`.

A down migration is itself a new reviewed migration. Live restore is a different planned operation under D8's operator-owned `allow_live_restore`, default false. Until that operation ships, P04-06's separately approved operator runbook governs live restore. The recovery decision records possible lost writes and establishes the restored schema before any app start. Neither an app rollback request nor a schema initialization/migration plan authorizes data restoration.

### Delivery and acceptance gates

No code changes implement this contract in P04-07. Today `spec.App`, `policy.Desired`, inventory and committed release state do not identify an app's persistent databases or current schema. The existing `persistent_roots` policy field is only an allowed-root list. It cannot distinguish a persistent app from a stateless app. Strict v1 specs reject undeclared data and migration fields. The executor and app rollback planner deliberately freeze the supported stateless desired shape, so adding persistent fields requires an explicit classification change rather than inheriting `stateless_compatible`.

P04-01 must carry the release declaration through strict parsing, normalization, canonical hashes, retained desired inputs, inventory, planning, presentations and apply revalidation when it adds persistent support. It must implement the missing-declaration refusal in the same change. Persistent support cannot ship first with this gate postponed. P04-07 remains unchecked until that behavior and the relevant T16 evidence exist. Migration execution additionally waits for P04-04 and P04-05, operator authorization and interruption tests.

Required tests include missing and incomplete declarations with no changes, incompatible candidate and rollback targets, unknown current schema, first-deploy initialization, config/start paths, removal of a data reference, unchanged stateless behavior, canonical declaration ordering and stale schema at apply. T16 also needs ordinary compensation, reconcile and resolve tests after an observed breaking schema change. Later migration tests cover missing, stale, mismatched or unavailable restore points, invalid protected policy, partial schema changes, interrupted journal boundaries and no blind replay or database restore. Mock coverage is not a verified S3-compatible storage point. A separately authorized physical drill must migrate a fixture, refuse incompatible app rollback without changing data, and verify an isolated restore of the recorded point from actual S3-compatible storage.

## Source references

The upstream manuals for [Quadlet](https://docs.podman.io/en/stable/markdown/podman-systemd.unit.5.html), [Caddy admin API](https://caddyserver.com/docs/api), [Litestream S3-compatible storage](https://litestream.io/guides/s3-compatible/) and [Litestream caveats](https://litestream.io/tips/) are useful starting points; implementation must pin and test actual installed versions.

## Operator enrollment

`brine enroll SSH_DESTINATION --finish` completes verification for an existing enrolled journal without replaying Apply. It requires the original public deploy key, a matching private key and the existing client identity-key file. Before restricted transport, a read-only host verification binds those keys, the installed binary hash and installed assets to the original protected enrollment. Missing packages or ID-map helpers refuse. The supplied host binary runs only as the operator's verifier and is never installed. Finish presents only verification, journaled fixture cleanup, empty runtime provenance and final private target writing. It retains the ordinary terminal target-name confirmation and cannot combine with `--undo`. The same host verification repeats after restricted ping, shell-command rejection and inventory; only then may existing verification cleanup and the normal target writer run.

Rootless Podman requires Debian `uidmap` and executable `/usr/bin/newuidmap` and `/usr/bin/newgidmap`. Inventory explicitly includes uidmap even when installing without recommendations. Its exact package version and dependencies enter the confirmed transaction and original package journal. Missing helper executables refuse runtime verification before any Podman query.

`brine enroll SSH_DESTINATION --target-name NAME --deploy-key /absolute/deploy.pub` uses the operator's existing OpenSSH configuration and strict known-host checking. It never uses the restricted deploy key for administration. Root SSH or non-interactive sudo is required. OpenSSH bootstrap uses fixed shell command strings with a strictly validated temporary-directory grammar; it trusts the already-authorized operator account that owns that temporary copy. It is not a root-owned staging area or an independent copy-to-exec content attestation. A differently sized or non-Linux client must supply a matching amd64/arm64 Linux executable with `--host-binary`. The private deploy key defaults to the public key path without `.pub`; `--identity` overrides that path. Only canonical Ed25519 deploy keys are accepted.

Enrollment refuses non-terminal stdin, `--no-input` and machine-output modes. `--yes` does not skip confirmation. The operator must type the target name after the complete change list. The matching binary runs read-only inventory before that prompt. Debian 13, cgroup v2 and the D2 runtime versions are required. Unknown listener, SSH, PAM or package state fails closed. The first non-comment main SSH directive must be the unconditional Debian `Include /etc/ssh/sshd_config.d/*.conf`, with no earlier-sorting drop-in. Another proxy on ports 80/443, preexisting unowned runner resources and other Caddy file imports are conflicts. A journaled root-owned runner-only SSH drop-in forces the dispatcher for every runner login and enforces `/etc/ssh/brine/authorized_keys/%u`, no authorized-key command or principals file, and no PTY or forwarding/tunnels. sshd evaluates later Match/Include syntax itself. Effective global `PermitUserEnvironment` must already be exactly `no` across IPv4/IPv6 loopback and remote connection specifications; enrollment never changes that global-only directive. Effective AcceptEnv may contain only LANG, LC_* and the exact display-only names COLORTERM and NO_COLOR; no additional wildcards or display-name prefixes/suffixes are accepted. SetEnv may set only concrete locale variables. Loader/shell/path/runtime variables and other patterns are refused before mutation. A bounded all-branch Include audit supplements the four effective probes because environment lists are additive; this audit conservatively refuses unsafe entries even for other users. Include accepts only literal absolute paths or the exact `/etc/ssh/sshd_config.d/*.conf` pattern, excluding dotfiles and sorting in the controlled C locale. Other patterns, character classes, `?`, braces, tilde, relative paths, ambiguous escapes and any Include inside an included file are refused. Read-only preflight and staged validation compare the complete audited unique source load order with bounded `sshd -ddd -T` debug output; unavailable, truncated or mismatched traces refuse enrollment. The complete staged SSH configuration passes `sshd -t` and all forced-setting probes before promotion and SSH reload. Known failure restores verified original state and reloads; interrupted/unknown reloads keep a durable pending marker and require explicit operator undo rather than automatic apply retry. Undo validates and reloads the restored configuration before removing protected key resources.

The operator-only hidden `host enrollment` endpoint accepts bounded JSON on stdin. It is not in the deploy dispatcher's operation allowlist. Its root-owned journal under `/var/lib/brine-enrollment` records intent before each mutation, runner identity, directory identity, file hashes, original file bytes/metadata, package versions and original Caddy service state. Reruns check observed state before applying a step. Directory intents explicitly remain pending until their inode has been durably recorded and ownership/mode changes completed. Interrupted creation is reconciled only for an empty directory under protected parents with the expected identity and root/bootstrap or planned ownership; unknown contents refuse recovery. Journaled original file bytes and metadata are a valid incomplete replacement state. A different binary or key is not an upgrade operation and cannot silently replace an enrolled one.

Missing packages use a simulated apt transaction with exact versions for every new dependency. Missing runtime candidates are checked against D2 before confirmation. Apply is bound to the displayed fixed change list, exact transaction and relevant inventory; it repeats the binding check before creating a journal and under the lock before performing enrollment changes. Changed candidates, dependencies, installed versions or host trust require a fresh inventory and confirmation. Enrollment refuses package upgrades, removals and transaction drift. It masks Caddy before installation, removes a new Caddy package's verified default site from the candidate root configuration, validates the Brine root and only then unmasks and starts the service. Existing root Caddy configuration keeps its sites and gains the one fixed import. The complete candidate is journaled and validated in the protected enrollment directory before live replacement. Known validation/promotion failures restore verified original bytes and metadata; unknown or changed observed state keeps the journal and fails closed. Interrupted promotion resumes from either the recorded original or intended file, and candidate cleanup is recorded. Package removal during undo is limited to recorded installed packages; it does not autoremove or purge preexisting package configuration.

Fresh enrollment explicitly requires Debian's `dbus-user-session` package. A
running, lingering user manager and installed `dbus-daemon` alone do not supply
the user bus that rootless Podman needs for systemd cgroups. The missing package
and its dependencies enter the same exact reviewed apt transaction; preinstalled
packages remain untouched. Container process binding still requires the expected
systemd user scope.

Private module descriptor and publication helpers admit two additional standard
OCI masks observed on the Debian amd64 runtime. `/proc/acpi` must be a read-only
tmpfs root with an actually empty, non-symlink directory of the observed root
ownership and mode. `/proc/kcore` must be a read-only, nosuid `/null` mount from
`devtmpfs`, with a non-symlink null character device, major 1/minor 3, mode 0666,
one link, zero size and the unmapped 65534 owner/group. Other extra paths and
writable or changed nodes still refuse before helper input.

The runner has a root-owned home and `.ssh`, with only runner-owned `.config`, `.local`, `.cache` and state below them. `authorized_keys` and the root-owned forced SSH command name `/usr/bin/env BRINE_AUTHENTICATED=deploy /usr/local/bin/brine host serve` with `restrict`. SSH AcceptEnv and SetEnv cannot supply that marker. The marker selects the enrolled runner principal; it does not independently prove an SSH login. A process already running as `brine` can set it locally and has that same principal's deployment authority. The host factory checks the captured marker before opening the store for every operation except ping. Run-op captures no marker and only receives the execution runner, not dispatcher planning or job-acceptance dependencies. The root-owned external deploy key is at `/etc/ssh/brine/authorized_keys/brine`; `.ssh` remains empty after verification. A root-written inventory identity key at `/etc/ssh/brine/inventory-key` supplies the shared snapshot collector without trusting runner-writable state. Caddy's generation parent is root-owned and group-writable by the runner so the unprivileged generation adapter can create and promote generations. The polkit rule grants only `caddy.service` reload.

Enrollment also journals root-owned `/etc/ssh/brine/operator-policy.toml` and `/etc/ssh/brine/requester`, both mode 0644. The requester is an opaque digest of the canonical deploy public key, never a request argument. The operator confirms a default policy with no allowed registries, domains or secrets, app ports 20000-20999, 512 MiB and 128 PID ceilings, a 1 GiB disk reserve and Caddy HTTPS port 443. Deployment stays denied until the operator edits that protected policy. Enrollment installs the default only if the policy is absent, never replaces it on re-run, and verifies file ownership and mode rather than content. Deployment still strictly parses and validates the contents. Undo removes only the unchanged enrollment-installed default; an edited or preexisting operator policy and its protected parent directory remain in place and are explicitly reported. The loader requires root-owned, non-writable parents and a root-owned regular file with no symlink or hard link. Policy TOML decoding is strict. The explicit `caddy_port` sets the local routed-health listener; production currently requires the explicit value 443, matching the generated Caddy sites. Other listener ports remain unsupported and are refused before deployment rather than failing routed health checks.

The host opens the secure store in `/home/brine/.local/state/brine`. Its control generation counts immutable committed releases; duplicate commits, plans and queued jobs do not advance it. Ping does not initialize the control store. Inventory reads the control generation without creating a database and recognizes zero only for an affirmatively empty private state directory. Quadlet directories are created only during a journaled executor step, not during enrollment verification or planning.

Connected image resolution uses bounded HTTPS requests and verifies the exact bytes for the pinned index, selected platform manifest and image config. Only Linux amd64 and arm64 are supported. It supports anonymous same-origin bearer token services and Docker Hub's fixed anonymous token service; it never reads operator registry credentials. The `docker.io` image origin uses `registry-1.docker.io` for the registry API. Only that API origin may use `https://auth.docker.io/token` with service `registry.docker.io`; the resolver binds the requested repository's pull scope. Redirects are limited to blob requests on the same origin, GHCR's fixed `pkg-containers.githubusercontent.com` blob host, or Docker Hub's fixed `production.cloudfront.docker.com` and `production.cloudflare.docker.com` blob hosts. Cross-origin blob redirects strip Authorization. Manifest and token redirects refuse, as do other origins, alternate cross-origin ports, and insecure redirects. Unsupported authentication refuses planning. Private registries that require credentials remain unwired.

`jobs.Runner` calls the production `Reconciler.ReconcileUnderLock(ctx, lock, currentOperationID, false)` adapter after taking its host lock and before loading its operation. The current runner is excluded. The error-only adapter discards the report and preserves errors. This host-lock-only path defers queued/launch-unknown records, rather than taking a launch lock in reverse order. The server factory supplies `Server.Reconciler`; `ExecutorFor` binds fresh facts to each operation's retained desired input using the normal executor adapters. The existing release adapter treats `store.ErrNotFound` as absence.

`reconcile` is an exception to new-deployment planning: it settles already-accepted immutable deployment intent and never relaunches an abandoned deployment. Its exact dispatcher arguments are `{"dry_run":true|false}`. Remote non-dry-run reconciliation records its own durable recovery-job intent under the short launch fence, launches the existing detached `host run-op ID` transient unit, and returns `{"status":"accepted","operation_id":"..."}`. Poll `brine status --target NAME --operation ID` for the job's state/events. The runner atomically claims the recovery receipt, excludes its own ID from inspection, and runs the recovery engine with the existing fifteen-minute job limit, independent of the fifteen-second SSH observer request. Success/failure is journaled on that receipt. Failure to acquire the locks before inspection or effects records `failed` with `lock_unavailable`, through the legal `preflight` transition; a timeout or error after recovery work starts remains `recovery_required`. An interrupted or uncertain recovery job is never blindly replayed. Local/boot reconciliation remains a synchronous bounded engine entry point.

Only dry-run uses read-only authorization, and it writes neither events nor runtime effects. Strictly validated preview intent reaches the factory before runtime initialization. The preview opens a bounded isolated read-only copy of the existing database/WAL while fencing existing launch/host lock files; SQLite may rebuild shared-memory metadata only in that disposable private copy. Source database, sidecar and lock bytes, permissions and mtimes are unchanged; no lock is created. The preview returns a report with `dry_run:true` and outcomes. If it cannot assess operations, an empty-outcome report explicitly includes `control_state`: `database_missing`, `schema_upgrade_required`, `schema_unsupported` or `preview_unavailable`. It never initializes or migrates the source database. Missing fences or an oversized/changing capture cannot produce a recovery assessment. Standalone reconciliation acquires the secure launch fence before a nonblocking host-lock attempt. Host contention releases the launch fence before waiting and retrying within the lock deadline. Once both locks are held, it settles launch-state records, then releases the launch fence before deployment inspection/continuation. Apply holds only the separate launch fence from operation creation through launch settlement, so an active deploy does not delay acceptance. Recovery-required is terminal. Success requires live committed-artifact evidence in addition to the durable release; safe continuation revalidates the plan before resuming only its unfinished suffix.

Enrollment's fixed change list includes root-owned `/etc/systemd/user/brine-reconcile.service` (0644) and `/etc/systemd/user-generators/brine-reconcile` (0755). The user generator adds a disposable default-target dependency only when running as `brine`; other user managers are unchanged. The oneshot calls `/usr/local/bin/brine host reconcile --json`, has a fifteen-minute deadline and no automatic restart. The local entry point refuses root and uses the enrolled runtime identity/policy checks. Enrollment journals both files and any newly created protected parent directories. Undo withdraws the generator before the unit, verifies recorded hashes/metadata and removes only recorded empty directories. Existing directories remain intact. This defines boot behavior; fixture/generator tests are not evidence of a real host reboot.

Before writing the successful client target, enrollment calls `ping` with the actual private deploy key through the pinned transport, repeats it with a shell command that must be ignored, and checks that a root-planted `.bashrc` did not execute. It also calls inventory through the restricted key, then removes the planted startup file and unrestricted home `.ssh/authorized_keys`, `.ssh/authorized_keys2` and `.ssh/environment` fixtures and repeats the host inventory. Podman’s read-only resource queries can create or update empty SQLite/lock metadata. Verification checks that containers, images, volumes and secrets are empty, accepts only the fixed metadata tree with runner ownership and no symlinks/hard links, and records its hashes and modes in the protected journal. Operator-confirmed verification may refresh that empty metadata record after repeating all checks; undo never refreshes it and refuses hash drift or any unknown/application data. Target files live in the private client configuration directory; no key or inventory goes into source control.

`brine enroll SSH_DESTINATION --target-name NAME --undo` requires the same interactive confirmation. Partial enrollment can be undone through authenticated operator SSH even when final client configuration was not written. A read-only undo-specific probe checks host trust and journal-associated runner ownership without requiring deploy SSH environment prerequisites; a subsequently unsafe AcceptEnv policy must not prevent protected operator cleanup. Undo verifies recorded hashes and directory identity, refuses app data or changed Caddy generations, restores the original root Caddy configuration and service state, stops the runner and waits for its processes/runtime directory to disappear. It reconciles actual account absence after `userdel --remove`, including exit 12, instead of treating the exit code as proof or retrying blindly. Only a verified Brine-created home is removed. The runner account/group, linger, runtime directory, subordinate IDs and processes must all be absent before success. Preinstalled packages are never removed.

## Read-only diagnosis

`brine diagnose [APP] --target NAME` returns a troubleshooting report without taking the mutation lock or changing app, route, release or operation state. It uses the existing pinned client transport. `--json` returns the usual success envelope containing the report. `--jsonl` returns one success envelope with `data.event = "complete"` and `data.report`. Neither mode prompts. A successful envelope means the report was collected, not that the host is healthy.

The dispatcher verb is `diagnose`, classified `read_only`, with exactly `{"app":"NAME"}` or `{"app":""}` for the host-wide report. Requesters cannot choose executables, unit names, paths, timeouts, log limits or policy inputs. Unit and container probes use observed Brine app units. Health uses the committed port and health path on loopback, with redirects and proxy environment settings disabled.

The report has its own `schema_version = 1`, `host`, `app_names`, `apps`, `truncated` and ordered `findings`. Every fact has `status` and either a typed `value` for `known`, or a stable `reason` for `unknown` or `absent`. Unknown facts do not trigger failure rules. Host-wide discovery merges observed and recorded app names; if either source is unavailable, `app_names` is unknown and any observed subset still appears in `apps`. Supplying an app selects that name without claiming it exists.

Collection has a 12-second total deadline, a four-second inventory deadline and one-second individual probe deadlines. There are at most four outstanding probes, including dependencies that ignore cancellation. A collection deadline produces a partial report with unknown facts; cancellation of the caller fails the request. Reports stop at 16 apps in name order, 10 recent operations per app, 20 lines and 8 KiB of encoded logs per app for each of the independent `logs` (container output) and `unit_logs` (unit journal) facts, with a 512 KiB total report ceiling. `truncated` identifies the app-count limit. Logs that exceed their own limit become unknown rather than publishing an unredacted or incomplete tail. Both collection and response decoding reuse `internal/logs` redaction. Redaction remains best effort, as with `brine logs`.

Host facts include OS, architecture, runtime versions, available disk and memory, runner identity, linger and Caddy generations. The selected on-disk generation is separate from the live generation. A live generation is known only when inventory proved equivalence of disk-adapted and active configuration and identified every selected app file among the live imported sources. Otherwise it is `unknown` with `live_generation_unproven`. Route presence uses the inventory's app association or the opaque identity of the app's fixed imported file, not a filename guessed from a served domain. These are observations, not an atomic host snapshot or replacement authority.

App facts include unit state, the unit's restart counter, container running state, direct HTTP health, recent operation states and safe failure codes, current and previous release IDs, artifact drift, routing and the redacted log tail. Drift reuses the app-status comparison of unit hashes, image/platform, host port, immutable secret bindings, the selected Caddy file and attributable live domains against committed state. Only this app's artifacts are compared; other sites' routes are not this app's drift. Required current secret name/ID pairs must be present, while retained rollback versions are expected leftovers. Proven differences, including a known-missing Caddy file, remain known drift even when other observations are unavailable. Known drift lists only proven differences, not a claim that every artifact was observable. Without committed desired domains, the live-domain comparison remains unknown. Missing artifacts are not invented. The restart counter is not described as a count since the last deploy.

The production host adapter reads an existing runner control database with read-only SQL. It does not create a missing database, migrate a schema, change permissions or obtain the mutation lock. SQLite may use WAL shared-memory bookkeeping. The production diagnostic reader loads the current protected operator policy to observe its disk minimum. If that read fails, the minimum is `unknown` with `policy_unavailable`; the report never substitutes a guessed default or the policy of an older release.

Findings come from a fixed rule table. Their stable codes are `disk_below_minimum`, `runner_linger_disabled`, `unit_failed`, `unit_restarting`, `container_stopped`, `health_failed`, `route_missing`, `artifact_drift`, `operation_failed`, `recovery_required` and `plan_stale`. Each carries `severity`, an optional `app`, fixed plain-language `message` and `next_operations`. Suggested operations are not executed and confer no approval. Target names remain `NAME` placeholders in the shared machine report; human output fills in the selected target. Rollback suggestions refer to the separate rollback workflow and never authorize rewinding app data.

Evidence is synthetic. Tests cover each rule, read-only database behavior, fake store/inventory/journal collection, pinned-client protocol round trips, human/JSON/JSONL goldens, hung probes, health responses and redirects, drift, live-source proof, partial discovery, limits and planted secrets. No real deployment, host health or recovery outcome is claimed by these tests.

Secret allocation journal writes each receive a fresh bounded detached context, independent of runtime-list and durable-version read deadlines. The transactional secret-version intent append is itself the name reservation, not a separate allocation write: either the reservation and intent commit together, or neither exists. Podman creation starts only after that commit; a failed intent consumes no version and causes no runtime create. Unknown committed reservations remain unavailable for reuse.


### Stateless application removal (P03-09)

`brine remove APP --target NAME` records a plan, not a deletion. Apply it through
`brine apply PLAN_ID --target NAME`. The authenticated mutating `lifecycle`
dispatch operation accepts only an app name and `remove_app`; callers cannot
supply paths, units, raw Podman/Caddy commands or a purge flag. D8 allows scoped
application removal, not deletion of unrelated software or persistent data.

A removal plan remains an ordinary deploy operation with lifecycle `remove_app`.
Its `removal` ownership baseline binds the immutable release ID, exact unit
names/hashes, owned route hash, complete observed routing generation/file set,
and live release identities. Ordered changes are `withdraw_route`, `stop_app`,
`remove_unit`, `retire_app`. Planning verifies recorded ownership and observed
artifacts. Apply rebuilds and compares the entire plan under the host lock;
stale queued plans fail with the closed `stale_plan` event before effects.
Unknown/unowned or drifted artifacts conflict instead of being removed.

The executor journals these steps in order:

1. `withdraw_route`: validate/adapt the complete Caddy generation, withdraw only
   the owned route, publish and reload through the existing polkit boundary.
2. `stop_unit`: settle systemd jobs and independently prove systemd and Podman
   have no live writer. Route withdrawal must complete before stopping.
3. `remove_unit`: recheck quiescence, verify the exact file hash and Brine marker,
   remove only the owned active Quadlet, and synchronize the directory.
4. `reload_units`: daemon-reload through the existing typed systemd adapter.
5. `retire_app`: recheck quiescence and atomically remove the matching live
   release head (freeing its port reservation) with an immutable removal receipt.

The unified schema v2 includes append-only `app_removals` receipts. Immutable releases and their
secret references remain. Removal never deletes Podman secrets: D5 retained
release references still own those versions. The response adds
`secret_retention: "d5_retained_releases"`, and human output explains retention.
No images, volumes, databases, app data or retained Quadlet history are deleted.
An old removal receipt cannot retire a recreated application's different head.
A genuinely uninstalled app yields a clean no-op plan; repeated removal is safe.
An observed orphan artifact is a conflict, not proof of an uninstalled app.

Active persistent incarnations, committed volume units and mounted-data
directives refuse with `persistent_archive_required` (exit 4) before effects
and explain that P04-08 archival is required. Active incarnation ownership is
loaded from the same control-store transaction as release heads; a missing
release is not proof of absence. Prepared, settled-empty and successfully
initialized incarnations without a release all refuse. Initialization attempts,
fences, allocations, replica bindings and restore receipts remain untouched.
This does not implement archival, retirement, expiry or purge.

Interrupted removal uses a read-only, ownership-bound contiguous-prefix
assessment under the same host lock as single-use continuation. Policy, target,
control generation, unrelated releases/routes, unit hash and writer/job evidence
must still agree. Killed intent boundaries are journaled as `unknown` before
settlement. Disk publication never proves a past Caddy reload: a fresh complete
validated generation and reload settles an interrupted route withdrawal before
any stop. Unknown file deletion is re-observed, then safely re-entered through
the content-addressed synchronized deletion adapter; daemon-reload is convergent.
Only the immutable retirement receipt proves a committed removal. Unresolved or
foreign state remains `recovery_required`; removal never invokes deployment
rollback, restores a route or restarts the removed app.

Evidence is synthetic unit/fake integration plus actual local SIGKILL of run-op
subprocesses at all five effect boundaries. This does not prove a working real
Caddy/Podman/systemd removal, SSH disconnect or physical-host reboot.


P03-09 review regressions: routing reconstruction uses strict routing syntax and
immutable committed desired input, not today's deployment authorization. The
complete candidate still binds owned hashes and is validated/adapted by Caddy.
Revoked registry/domain/resource/secret permissions cannot authorize a redeploy,
but must not block withdrawing a committed app or retaining another owned route.
Interrupted no-op removal has an absence-validated completion path: fresh exact
replanning under the lock must still match before its receipt settles succeeded.
Unknown presence, changed generation/policy, or an unexpected effect event stays
recovery-required. Persistent-directive keys are whitespace-trimmed before checks.
Removal receipts were introduced by the v1-to-v2 migration. Schema v3 now adds
explicit terminal-resolution operations through a separate v2-to-v3 migration;
fresh enrollment creates the current schema.

### Terminal recovery resolution

`brine resolve OPERATION_ID --target NAME [--idempotency-key KEY]` accepts a
new detached operation for a terminal `recovery_required` deploy or immutable
secret assignment. Deployment plans include updates, rollback plans, config
changes and stateless removal. The machine request is exactly
`resolve {operation_id, idempotency_key}`; it has no force, shell, purge or
operator-authorization switch. D8 mutating authorization and current per-app
policy still apply. Operator-only actions remain outside this command.

Terminal receipts stay immutable. A `resolve` successor has `recovery_of`, a
value-free `resolution {operation_id}` event and an atomically adopted, bounded
step/secret-version prefix. Idempotent acceptance and the launch fence prevent
duplicate runners; the store transaction fences active or successful descendants
across the entire receipt family, not only direct successors of one source. Schema v3 migrates the real v2 operations/journals without
rewriting them and preserves the v1 migration path.

The successor loads the original stored plan, acquires the existing host lock,
then inspects current ownership, policy, artifacts, routing, writer and systemd
jobs. It does not generate a new plan that mistakes earlier journaled Brine
withdrawal for foreign route drift. Ordinary planners remain strict. Safe
resolution reuses executor continuation/rollback and journals all new effects
under the successor. Recovery authorization freshly settles manager jobs and
proves the unit/container writer state even when the last step was a read-only
health check. Rollback must prove the live unit hash belongs to the candidate or
recorded predecessor before stopping its service. Repeated attempts validate at
most 64 immutable, same-plan recovery-required ancestors under the lock. An exact commit/retirement receipt
from any member stays usable after a later resolution failed to journal its
terminal state; missing, foreign, cyclic or overlong chains refuse.
`run-op` and reconciliation recover interrupted successors. A durable phase
transition can precede its step intent: recovery preserves an already-reached
phase and never advances past the phase justified by the journal prefix;
preview remains read-only. Successful removal finishes deletion/reload and
retires the live head and port, without restarting the removed writer or
restoring its route. Retained history, images and secrets are not purged.

An affirmatively installed candidate before route publication can be rolled
back after the original deployment stopped: restore the owned previous unit and
prove health, or remove the first-deploy candidate when there is no predecessor.
Application rollback does not rewrite databases. An exact committed release or
removal receipt can settle success only with the existing live-artifact checks.
An uncertain secret assignment only reads the recorded immutable version name:
present settles succeeded, definitely absent settles failed, unknown remains
recovery-required. No value is read and no secret creation is replayed.

This is not unconditional repair. Ambiguous ownership/writer state, changed
policy/control generations, unproven failed/rollback prefixes, unavailable
staging or loaded-generation evidence, and uncertain deployment route
publication still refuse without further effects. They need an operator to
establish authoritative state; no DB editing, arbitrary host command or
permission bypass is provided. A refused successor is itself recovery-required;
resolve that latest receipt after fixing the underlying evidence, not an older
ancestor that lacks the newer journal. Human operation status conditionally
suggests resolution inspection and diagnose directs the reader to status; the original terminal receipt intentionally remains
visible, while the successor status and `recovery_of` report the repair outcome.

Human status only offers resolution inspection for a complete supported forward
or removal prefix. Unproven failures, completed rollback prefixes, missing evidence, uncertain deployment route
publication and partial journal pages do not receive unconditional resolve advice.
Diagnose lacks step-prefix evidence and directs the operator to status instead.
Resolution creation inspects at most 64 ancestors and 4096 family receipts;
excessive or inconsistent families refuse rather than widening authority.


Effect-time writer fencing applies to ordinary deployment and rollback as well
as resolution. After durable step intent and before stopping, replacing or
restoring the owned unit, reloading it or starting a writer, the shared executor
reads the active Quadlet through the pinned-root ownership adapter and settles
systemd jobs with independent unit/container probes. File ownership is checked
again around those probes; unknown ownership or unsettled jobs refuse before
the requested effect. Restore/reload/start boundaries require a stopped writer,
and a successful stop must also prove the writer stopped with no queued job.
A previous writer already running after a failed stop is health-checked without
issuing another start. Proven pre-effect refusals are failed step outcomes, not
unknown effects eligible for applied-effect readback.

Recovery compares the current policy version and hash with the stored plan
before authorizing any continuation or rollback, even if the app remains allowed.
A proven pre-effect refusal journals the closed `effect_refused` step code.
Inspection may remove only its matching durable intent/refusal pair from a private
projection; original receipts remain unchanged. Generic `rollback_failed` is not
proof of an unattempted effect. After fresh ownership, routing and writer/job
checks, resolution or reconciliation can continue from the true preceding prefix.
An ordered, completed pre-publication rollback prefix may be retained without
replaying its completed effects; compatibility proof, restored predecessor
artifacts and writer state are checked. Ambiguous, noncanonical or still-pending
rollback outcomes remain recovery-required. Read-only failed health probes can
be inspected again; uncertain mutations are never converted into refusals.


### Combined first-deployment review

`brine deploy brine.toml --target NAME` composes the existing preparation,
credential delivery, initialization and deployment operations. It never creates
backup credentials or runs application code as a schema initializer. The client
compiles every declared schema before contacting the target. One review shows
allocation, delivered credential status, exact SQL/catalog and the intended
release. `--yes` approves execution; `--json` without it returns the review only.
`--no-input` without it never changes application or data state.

The read-only `deploy_state` request uses the same strict spec/artifact metadata
shape as connected planning. It returns a review hash binding normalized
desired input, target identity,
image resolution, committed releases and the same non-data admission facts as
release planning, plus affirmative preparation, schema, credential and fence
facts for each configured database. Missing reservations are not unknown observations. Unknown
schema or prepared-data observations refuse; they never authorize initialization.

Dependent immutable plans cannot exist before their prerequisites. The client
builds them in order after approval and rechecks the approved decision-facts
hash before each new effect. The review hash uses the same listener-state and
port-admission projection as ordinary release planning, so out-of-scope UDP churn between preparation,
credential delivery, initialization and release cannot invalidate approval.
Workflow-produced data observations and incarnation reservations are excluded
from that hash; the immutable data plans and engines validate those independently.
A changed release, route, relevant port, generation, policy or other admission
decision refuses with `conflict`
and exposes the new release diff instead of silently changing the approved
release. `--yes` and machine modes stop and require a fresh invocation to
review and approve it. An interactive release presents the new diff and asks
again before using its exact plan hash.
It carries the exact locally reviewed initializer,
and the host independently validates its retained plan. A changed policy or
spec stops the sequence; the original reviewed intent is not silently replaced.

Each step has an independent host operation journal and detached job. The client
also retains only plan IDs, operation IDs, idempotency keys and completion flags
in a locked 0700 resume directory with a 0600, atomically replaced journal under
the selected target configuration directory. Its name binds the pinned target,
original spec and schema artifact metadata. No credential packet, SQL or spec
contents are stored in that journal. A lost acceptance response uses the read-only `deploy_operation` lookup, bound
to the server-established requester, original plan and deployment key. It
recovers the existing operation ID without resubmitting a mutation or secret
packet; ambiguous task attempts refuse. Host journals remain authoritative;
the client journal only remembers which jobs to observe. Every checkpoint
reference is read back through `deploy_checkpoint` and bound to the actual app,
target, normalized desired input, database and step. Retained preparation
reviews come from their host plans, never fabricated client plan summaries.
Operation references, including completed ones, must match the app and retained
plan before the client uses them.

A rerun polls unfinished work before advancing. Explicitly settled failed or
rolled-back work can receive a fresh plan only after affirmative unfenced host
observations; recovery-required or unknown work is never blindly replayed.
Initialization still allows one mutation attempt per immutable plan. A held
fence requires the existing inspect/reconcile flow, not a new initializer.
Initialized and stateless applications use ordinary release planning/apply.
Machine results, including interrupted/failure results, list the accepted
operation IDs for every step so observers can inspect independent host journals.

When fresh data needs credentials, `--backup-credentials FILE` accepts only a
bounded regular 0600 file in the same JSON packet format as `backup credentials
set` stdin. Existing delivered credentials are reused. Missing credentials stop
before apply and print a copyable deploy command naming the required file.

### Retained archive maintenance requirements (P04-08)

Retained maintenance plans bind the original archive plan, immutable retirement timestamp and thirty-day deadline, current enrolled requester/policy/target, and the exact archives-parent device/inode for every recorded tree. Early purge requires a distinct reviewed purge plan and apply. Expiry refuses before the exact deadline. A saved plan is not execution authority: recovery requires a durable deletion intent. Deletion journals each tree's intent and receipt; an unknown outcome inspects/adopts only that tree and namespace. A replaced archives parent cannot make an undeleted bundle appear absent. Traversal uses pinned no-follow directory handles and refuses symlinks, hard links, special files, foreign devices/ownership, unsafe modes and bounded-traversal overflow. Host and original replica lifetime locks precede the first deletion intent. Retirement, manifests, bindings, fences, remote prefixes and maintenance history remain immutable. No maintenance interface has remote-delete authority.

Renewable archive credentials have a distinct archive/binding/epoch scope and immutable private namespace, never an app-name or current-replica lookup. Their metadata records original destination/credential references, current authorization, version, received time and supplied expiry without secret values. The archive delivery service has no replica activation interface; it does not pretend a retained binding is unfenced. Unknown delivery adopts only exact sealed plan/scope metadata and identical private environment bytes. An environment without its receipt remains a reserved, unusable version. Renewal does not extend the original retirement deadline. Fresh retention verification must independently restore the original remote source and bound proof validity by the original deadline, credential lifetime and current protected destination-retention evidence. Failed checks preserve the timestamp of the previous proof rather than relabeling it fresh.

The restricted enrolled dispatcher exposes the same services through public
target commands:

- `brine archive maintain plan ARCHIVE_ID --kind KIND --target NAME` records
  reviewed `verify_retention`, `purge_archive` or `expire_archive` intent without
  provider or deletion effects. `maintain apply ARCHIVE_ID --plan PLAN_ID`
  submits the exact plan; another archive ID cannot select it.
- `brine archive credentials plan ARCHIVE_ID --binding BINDING_ID --epoch EPOCH_ID
  --target NAME` reviews renewal with optional `--expires-at UTC_TIME`.
  `credentials set` takes the same archive/binding/epoch selectors and
  `--plan PLAN_ID`, with bounded private JSON on stdin. No replica activates and
  no secret enters argv or output.
- `brine archive status ARCHIVE_ID --target NAME` reports unverified, verified,
  degraded, expired, deletion_pending or local_deleted and preserves the last
  verification timestamp. It never probes the provider or refreshes proof age.
  Missing/expired credentials, unknown current destination retention and stale
  verification are not healthy.
- `brine archive maintain resolve OPERATION_ID --target NAME` derives the exact
  original archive and plan from the retained maintenance operation. It
  inspects an already-journaled unknown local deletion, adopts known effects and
  completes only its admitted original scope. The original operation outcome
  stays unchanged. A reviewed plan or missing path alone supplies no deletion
  authority.

Maintenance apply and resolve use detached jobs with saved idempotency keys and
operation IDs. `--no-wait` returns acceptance only. A terminal receipt binds
`kind`, `operation_id`, `original_operation_id`, `plan_id`, `archive_id`,
`maintenance_kind`, `outcome`, `evidence_sha256` and `completed_at`.
Verification reports `verified`; purge/expiry report `local_deleted`.
Native retirement keeps its separate `archive native inspect`/`resolve` flow.

Maintenance admits the original prepared root, exact original held fence and current destination/target policy, checks the original replica managers and both source/archive mounts, and ignores a fresh unrelated app-name writer. Initialized verification independently restores the original source into a private isolated workspace. Original exact-version credentials remain usable until renewed; archive renewals select only their exact sealed original scope. Physical/provider acceptance remains required; P04-08 and T22 remain unchecked.

Retained local deletion never crosses mounts, including same-device bind mounts. Directory opens below the admitted root require Linux `openat2` with `RESOLVE_NO_XDEV`, `RESOLVE_NO_SYMLINKS` and `RESOLVE_BENEATH`; namespace and traversal checks require known matching `statx` mount IDs. Preflight rejects mounted live data before unlinking any archive entry. Unknown mount evidence or unsupported kernel facilities refuse deletion; there is no weaker traversal fallback.

Reconciliation snapshots the single tree index whose deletion intent was already in flight when it starts. Only that tree may adopt absence as an unknown prior effect. Later trees in the same run require actual deletion; unexplained absence refuses with recovery required, even after this run writes their intents.

Residual same-identity filesystem race: pinned directory handles do not prevent the owner's own same-UID processes from renaming an already opened subdirectory out of the archive on the same filesystem. Purge can continue unlinking that original directory's contents through its pinned handle after the rename. Brine's lifetime locks coordinate managed writers, not arbitrary same-UID filesystem changes; the archive walker is not a sandbox against the enrolled identity. The owner must avoid manual moves or other unmanaged mutations of archived directories during purge. Mount-boundary checks remain enforced, but do not provide continuous pathname containment against this rename race. Additional ancestor observations would themselves race with the next unlink, so this remains an explicit residual rather than a claimed isolation guarantee.

## Managed engine startup primitives

The owned SpacetimeDB store, secret versions, foreground service, launch guard, lifetime lock, and copied-native boot proof are specified in [Managed engine startup](managed-engine-startup.md). These internal primitives do not enable ordinary deployment, first publication, or live restore. Existing SQLite startup and canonical records retain their contract.

Native live restore activation keeps the authenticated download intact. Under
one retained restore intent, the host verifies the selected immutable point and
reader disposal. Under the following transition intent, it copies only native
checkpoint/history members into a separate staged tree and recovers the exact
historical signing version through the existing immutable material writer. The
protected target record binds the actual rooted staged inode, original inode,
allocation, runtime, checkpoint, material readback and owned service readback.
An existing partial staged tree is preserved and cannot be overwritten.

The restore adapter derives its actual16 journal input from actual20 records;
requests cannot supply a transition, proof, repository scope or target path.
Preserve/promote moves use the existing rooted no-replace rename/fsync helpers.
Recovery adopts a positively observed retained movement rather than repeating
an unknown movement. Activation atomically settles actual20 progress and the
new epoch without installing a live-start grant. Completion requires fresh
original/target layout, owned material/service readback, compatible retained
code and absence of engines, application writers, ingress and private readers.
It completes actual16 and actual20 and releases only the original owned fence
in the same transaction. Existing-transition resolution holds fresh real guards
and validates the immutable original claim, consumer, fence and current16
layout; it never reopens an expired stopped-source mutation permit.

The native restore commands below expose these guarded effects through the
qualified backup runtime and existing operation adapter. Storage/helper tests
alone are not physical restore acceptance.


## Protected workspace consumer observation

Fresh enrollment retains the actual runner UID/GID before installing
`brine-workspace-observer.service`. The fixed service may establish its private
listener during enrollment, but every request requires a protected record whose
phase is exactly `enrolled`, matching kernel peer/message credentials and the
current protected persistent roots. Listener startup supplies no request grant.

The separate root-owned `/etc/ssh/brine/workspace-observer.toml` accepts only the
boolean `allow_workspace_observation`, default false. Missing, omitted, false,
malformed or unprotected permission denies observation. Enrollment installs false
when absent and preserves a valid existing grant. It changes no deployment
operator-policy hash. The fixed root:brine service uses the private AF_UNIX
`unixpacket` socket `/run/brine-workspace-observer/observe.sock`; public dispatch
cannot start or configure it.

One passed runner-owned `0700` directory descriptor selects only a
`native-restores/<32 lowercase hex>` workspace, the exact `native-restores`
parent, or `apps/<32 lowercase hex>/databases/<32 lowercase hex>` under the
current admitted root. Arbitrary owned subtrees and descendants refuse. The
normal source caller separately binds its actual protected allocation and
stopped capability. A fresh nonce and actual device/inode/mount bind the
`absent`, `busy` or `unknown` result.
No request selects a path, PID, policy, command or caller-supplied proof. The
service preserves mount-namespace identity and bounds capabilities to
`CAP_SYS_PTRACE` and `CAP_DAC_READ_SEARCH` for read-only process metadata.
The fixed syscall sandbox denies `ptrace`, `process_vm_readv`,
`process_vm_writev`, `kcmp`, `kill` and `pidfd_send_signal`, plus mount/reboot and
namespace changes. It has no execution or deletion action.
Actual installed sandbox and FDSTORE qualification remains pending; source
review does not establish those physical results.

Backup inspection/resolution cleanup disposes only identified stopped private
readers, independently proves process-consumer absence, and records an owned
retirement intent before deleting the held original workspace. Intent binds the
original operation, canonical descriptor and directory identity. A later missing
directory can settle an issued retirement; missing without that matching intent
or a replacement directory remains unknown. The intent itself is not an absence
proof. Inspection and review join cleanup errors into their returned error, even
after remote verification. Only actual successful cleanup can support a success
view or resolver outcome.

## Native backup command execution

The reference-only native backup commands are described in
[native-backup-commands.md](native-backup-commands.md). Their host controller
binds the actual requester, current managed binding and protected policy before
submitting work through the existing detached job engine. Recovery passwords
are written privately before submission; operation journals contain references
and hashes only. Native S3 credential delivery uses actual native archive scope
and does not construct a SQLite replica.

Routine capture holds the binding launch guard while the native writer remains
running. It copies selected owned history, checks captured prefixes independently,
and derives the actual sole replica and bounded compressed expansion sizes.
Current module/control observations and the fixed passive native reader precede
upload. The selected archive includes exact native-source metadata and encrypted
signing recovery material. Authenticated remote extraction, complete persisted
state equality, confirmed lower-bound inclusion and final-pair equality precede
an immutable historical point. Later loss is unknown.

Exact preparation additionally retains its original fence and clean-stop
observations, withdraws hash-owned ingress under the host lock, and holds the
native lifetime lock. Its completed source reloads the retained generation and
exact bundle, not a newly generated checkpoint. The actual initial-owner helper
supplies unpublished publisher provenance; JWT syntax does not supply it.
Recorded managed runtimes and plans retain their exact pinned engine identity.
Decoding validates image, manifest and configuration hash syntax and the closed
engine version without replacing those pins with the current published profile.
New allocation and initial material admission require the current selected
image and platform manifest. Native pulls use the admitted plan image; module
extraction and staged publication inspect the recorded index, platform manifest
and configuration ID together. Missing identity proof refuses the operation.
Existing allocations have no implicit engine update.

Initial unpublished preparation adopts only the exact original allocation fence, proven
by the successful managed preparation, immutable plan and allocation, and
completed initial material delivery matching the protected runtime. The new
preparation operation owns its effects; the original allocation operation still
owns the fence. Every stopped-source, completed-point, publication and resolver
read keeps those identities separate. Deriving ownership from protected records
preserves existing canonical journal bytes. Missing material, foreign or replaced
fences, and unresolved preparation claims refuse a new action. A terminal failed
operation without a preparation claim remains failed and supplies no success
evidence. Initialized preparation still acquires its own new fence.

After terminal source preservation releases a consumed unpublished fence, a
distinct normal preparation may acquire a new operation-owned fence. Under the
existing binding guard and one write transaction, admission validates every
current-epoch consumed publication's preserved cursor/proof, immutable original
failure and exact succeeded resolver receipt. The current unpublished runtime
head and stopped desired state must remain bound to that preserved source. The
released fence stays released. Only the new owning operation may reuse its held
fence; unfinished or foreign consumers refuse. Initialized preparation is
unchanged.

Historical restore-test reads the exact retained point and verifies in private
isolation without starting or replacing live data.

Native backup activation records repository creation before invocation and
requires opaque authenticated configuration readback. A pending activation
blocks another creation attempt for the binding. Fixed unit and timer ownership
and actual loaded timer properties precede active policy. Transport deadlines
stay within both the caller budget and admitted material expiry. Enrollment
installs the independently published main native-reader0.0.5 platform pin;
reader execution pulls its fixed digest only after affirmative local absence.
Stopped unpublished preparation can be inspected before any upload exists.
Its original identity binds the immutable preparation intent, first stop proofs,
and failed outcome. A `stopped_unpublished` observation requires the exact
original allocation, config, material, held fence and closed routes/units, plus
a complete empty staging parent with no process, mount or descriptor consumers.
Retained initial-stage journals must be disposed. Journal absence alone supplies
no authority. A fresh stopped observation also binds each previous failed
resolver's exact immutable operation, failure outcome and claimed review under
the same producer, requester and app. Active, foreign or unsettled claims refuse.
Only the currently checked opaque claimant excludes itself, keeping the reviewed
hash stable after task claim. Previous failures and claims remain historical;
a new independently reviewed resolver cannot rewrite or replay them.
Private launch refusal records a closed command outcome as
`container_start_failed`, `container_start_unknown` or `container_start_output`.
An uncertain launch stays uncertain. Cleanup preserves the primary failure;
`helper_disposal` describes cleanup failure only when no earlier failure exists.
These codes contain no subprocess output and grant no replay or cleanup authority.
A reviewed resolver continues that original point and preparation
operation through newly journaled private staging and capture; it never creates
another preparation, repeats a recorded upload, or rewrites the failed outcome.
Actual remote verification and original producer completion still precede an
adopted resolver receipt. Unknown helper or stage ownership refuses continuation.

Unknown effects preserve their intents and partial files. Reference-only
`native_backup_inspect`, `native_backup_review` and `native_backup_resolve` use
fixed readback and an independent resolver task. Schema22 retains original upload
source/workspace descriptors, fresh expiring reviews and resolver completion rows.
Original outcomes remain immutable. Effective success requires the actual
successful resolver outcome, exact original scope and real producer completion;
a completion row or well-formed receipt alone is insufficient. Cleanup follows
durable producer/resolution proof and precedes the success reply. Planned data
mutations exclude unfinished uploads without asserting quiescence or stopping
ordinary writers. Partial or unprovable effects refuse and retain their resources.
Physical phase acceptance remains pending.

## Native material delivery

`native_material_set` is a synchronous, mutating, private-stdin operation with
closed app/preparation-plan/idempotency selectors and four fixed roles. It is
not a detached task and cannot queue plaintext. Schema 23 retains immutable
metadata-only material reservations and terminal receipts through the existing
operation/event journal. Host failures retain their candidate files and versions
for inspection, never overwrite or resume an unknown write. `native_material_status`
is read-only and bound to the server-established requester.

The first delivery requires the genuine never-started managed allocation under
its positively existing original fence. It commits only configured-unpublished
runtime expectations: native database/publisher identities stay empty and names
remain empty. Signed JWT claims and desired module metadata grant no native
initialized authority. Later deliveries remain unreferenced by the active head;
material activation is a separate reviewed operation. See
[native material commands](native-material-commands.md) for the packet and flow.

### Passive discovery after unknown first publication

The pinned passive reader request has a required closed `identity` operation.
`known` requires the protected database and publisher identities and keeps the
existing exact identity/program/history constraints. `discover` requires the
protected publisher identity and forbids any supplied database identity.
Discovery reconstructs one selected replica with complete bounded fail-fast
native replay, reads exactly one persisted `st_module` row, and verifies its
owner. The observed database identity must also match the actual selected
snapshot identity when present. The strict full-state observation then verifies
that actual identity, owner, current program bytes and hash, complete history and
tables. Neither operation starts a module, reducer, hook or scheduler.

The host must already own and contain a quiescent app-specific private history
copy. It verifies the exact pinned reader, unchanged copy and reader disposal.
Discovery does not establish a logical database name or release a publication
fence. Missing, partial, ambiguous, multi-module or corrupt history leaves the
original publication unknown. Current request producers change together with
the reader; there is no old-request fallback.
### Native live restore execution

`restore native-live plan APP` selects one immutable verified remote point, one
fresh exact prepared current point and one compatible retained recovery release.
The default-false live restore policy applies to both plan and execution. Apply
submits `native_live_restore` through the existing detached task runner. Requests
contain references only. The host reopens the completed original stop under actual
launch and lifetime locks, copies and checks its retained checkpoint, records one
download intent and verifies the authenticated extraction with the pinned passive
reader. Any uncertain download retains its stage and fence and cannot be retried.

Target preparation preserves the download, exact signing versions and original
owner. The shared incoming repository lease records one initialization attempt,
then authenticated config readback. Rooted no-replace moves journal preservation
and promotion before epoch activation. Fresh loaded-route absence, service jobs,
container/process absence, owned units, secret files and installed compatible code
are checked before activation and completion. Completion retains the original
whole tree, releases its original fence and leaves the new epoch stopped. The
restore retains an immutable code intent naming both the original current release
and the reviewed recovery release. With the original fence and both host guards
held, it verifies the recovery image and retained secret versions, then uses the
existing Quadlet staging and replacement to install the reviewed frontend while
preserving its predecessor. Engine-only recovery removes and retains the prior
frontend. No service starts or route publication occur. Epoch activation selects
the reviewed release head in the same transaction. An interrupted replacement
can settle only an observed original or reviewed artifact under that same intent.

`inspect` reads retained progress. `review-resolution` binds a fresh immutable
review to the original operation, journal head, transition input and actual layout,
material and code observations. `resolve` uses its own task operation and guards,
keeps the original recovery-required outcome unchanged and completes known moves
through observed layout and fsync adoption. It never repeats extraction or
repository creation. Missing target preparation, uncertain extraction or an
unexplained layout refuses resolution and retains the fence for manual inspection.

### Native archive retirement

`archive native plan APP` reads an existing exact prepared point protecting the
current initialized or positively unpublished native engine, every retained backup epoch and every terminal
engine transition. Nonterminal transitions and unreserved incoming repositories
refuse planning. The plan binds complete local transition-tree identities and
retention evidence. `apply` submits `native_retire` through the existing task
runner and uses the existing ordered archive service.

Native retirement consumes the original prepared point once and keeps its original
fence owner. A protected native archive verification row joins the actual stopped
preparation, current head, authenticated extraction, passive replay, confirmed
marker, signing/recovery bundle and known reader disposal. Coverage JSON alone
cannot authorize retirement. The owned engine service is retained locally before
retirement; recovery secrets and remote repositories are preserved.

After the application tree moves, each complete terminal transition directory
moves into the archive's `native-transitions` directory through a separate durable
intent, rooted no-replace rename and parent fsync. Retained pending moves can only
be adopted from positively observed destination identities. A pending source-side
move is not blindly retried. Final retirement requires every transition move and
sets the original thirty-day deadline only after all moves complete. SQLite
archive records retain their original canonical encoding.

`resolve OPERATION_ID` uses a separate `native_retire_resolve` task and preserves
the original recovery-required outcome. With the original manifest and real host
guards, it checks current unit ownership, loaded frontend jobs/processes, private
reader absence and original source bytes. A new authenticated read and passive
replay verifies the original bundle. Known completed unit/tree moves are synced
and adopted; uncertain preparation without a manifest remains fenced.

Archived repository reads have a separate capability joining the immutable
manifest, canonical point dependency, original consumer/fence and exact retained
bundle. Retired maintenance does not require an active incarnation or reuse an
expired preparation permission. Eligible archived credentials and the original
recovery version bound every read. The transport deadline ends before the
credential authority. Retention receipts come only from authenticated extraction
and, for initialized data, fixed passive replay with known reader disposal.
Store receipt writes and reads independently join their protected replay row.

Positively unpublished retirement retains the actual owner/config/metadata and
signing-recovery source under authenticated extraction. It requires absent
frontend artifacts, processes and loaded routes. It neither creates a database
nor substitutes initialized schema, marker or offset evidence.

No physical native retirement, successful R2 restore, post-archive recovery or
purge acceptance is claimed by command, structural and local filesystem tests.

Early archive purge requires the separate `allow_archive_purge=true` operator
policy grant. Omission and explicit false preserve the previous canonical policy
bytes and deny purge. Live restore does not grant purge. Plan and execution both
recheck the grant; revocation prevents new deletion effects. Expiry remains bound
to the original thirty-day deadline. Written/native early purge also requires a
fresh protected retention proof, whose canonical hash is retained in the original
deletion intent. Recovery preserves that intent and proof and checks the current
grant, policy and retention before each further destructive step. Neither purpose
deletes remote objects.

The enrolled Brine runner uses the root-owned canonical
`/etc/brine/runner-storage.conf`, selecting overlay with the fixed
`/usr/bin/fuse-overlayfs` mount program. `CONTAINERS_STORAGE_CONF` may name only
that source constant. Actual NSS runner identity, not a supplied session UID,
selects the profile. Runtime admission verifies protected canonical bytes,
helper provenance and device access, the qualified helper version, and actual
Podman dynamic UID mapping. Native, application and replication service
launches share this profile without exposing it as application configuration.
Enrollment previews and journals the required FUSE package and protected file;
undo preserves its existing unknown-data and ownership refusal rules. A native
store cannot safely revert after FUSE layers are written. No startup, capture,
maintenance or general subprocess deadline changes with this dependency.

Owned native process binding uses the fixed no-input contained
`engine-mount-profile` helper to verify namespace-local mount identities when
the runner's namespace cannot traverse a FUSE root. This internal helper is not
a dispatcher operation and accepts no image, path, command, role or input.
PIDfd lifetime, start ticks, process command and executable, cgroup, exact
container identity and policy remain mandatory. A bounded strict frame binds
PID 1 and its namespace identities to fixed member metadata. Held host
descriptors, authoritative kernel UID and GID maps, and kernel overflow IDs
establish the exact corresponding namespace view. Regular-file identity and
size remain stable; directory size and link count can change with live writes.
The final inspection rechecks process and descriptor lifetime before any
credential input. A live engine carries the same pinned read-only `/brine`
helper mount as private verification containers. Unknown or mismatched evidence
refuses binding without retry, root namespace entry or weakened guards.

A private module-stage refusal frame remains a refusal when `podman attach`
exits successfully but the contained helper exits unsuccessfully. The controller
recognizes only the existing strict value-free diagnostic enum before cleanup
and preserves it as the primary failure. Malformed output grants no observation
or owner proof and cannot replace the original typed execution error.

The directly spawned native child can still be changing its executable or
arguments when process creation returns. The guest retains its PIDfd, start
ticks and cgroup while waiting for the exact pinned command. Binding and
listener readiness consume one configured startup deadline. The child keeps
the enclosing lifetime context, so ending readiness does not kill it. Before
any publisher or module API call, the guest rechecks the original process,
parent, namespace and data/key identities and final process lifetime. An exited
child, changed identity or expired deadline cannot supply native evidence.
The live engine's initial handoff uses the same command wait while retaining
its first owned container and process identity. It holds the host descriptors
and namespaces throughout, checks every other binding predicate before the
wait, and repeats full binding after the command matches. Routine live
observation and stop still require an immediate exact command match.
An earlier binding or observation refusal remains the primary phase if stopping
the child also fails.

An unpublished preparation can retain a verified initial publisher owner before
its upload intent exists. Inspection verifies that immutable owner against the
original stopped preparation, current runtime and material, unchanged source
seal, exact recorded generation and reconstructed sealed source hash. The review
binds the owner receipt. This phase requires no upload descriptor, upload intent,
prepared point or capture input, and all earlier stages and consumers must be
settled. Resolution reuses that owner and generation without another publisher
helper, owner write or preparation operation. It tracks the new copy workspace
and preserves it once an upload is issued. Inspection never retires a workspace
that is the retained original upload; its later authenticated retirement also
settles the workspace journal.

Absent module payloads and native evidence for an unpublished point are empty
SQLite blobs, not NULL. Their hashes remain the hashes of empty bytes. Required
source, manifest and transport records and all initialized validation remain
unchanged.

Publication review derives completed preparation authority from the validated
prepared point. A succeeded resolution receipt can prove that point while its
original producer remains `recovery_required`. Review does not require or
rewrite that original outcome. The point reader still joins the exact resolver
receipt, completion journal, point hash, binding and epoch. Missing or changed
resolution proof refuses review; the existing expiry, fence, requester, app
and declaration checks still apply.

Reopening a validated prepared point acquires its recorded fence owner. For an
unpublished allocation, that owner is the original allocation operation and
can differ from the later preparation producer. The guard verifies the exact
binding, fence and held owner. It does not substitute the producer or another
owner when they differ.

State-root source copies and publication reader views create their fixed private
`staging` parent through the protected directory writer before use. Existing
owner, mode, symlink and filesystem checks remain required. Each consumer keeps
the held parent identity. After disposing its own copy or view, it removes a
parent it created only when that same directory is empty. Pre-existing parents
and replacement directories are preserved; unknown consumer cleanup cannot
retire the parent.

When the CLI has validated an accepted asynchronous task and waiting later
fails, its error envelope keeps `data.status="accepted"` and the original
`data.operation_id`. `ok` remains false, and the original error code and exit
status remain unchanged. Human diagnostics also include that operation ID.
Acceptance is only a locator for status and recovery; the CLI never resubmits
work, emits remote diagnostics or trusts an operation ID from a crossed poll
response.

Publication effect `unknown` events may carry `failure` with only a closed
`action` and fixed `code`. Codes identify contained guest or controller result,
disposal and source-check boundaries. They carry no subprocess text, credential
or module content and grant no publication or recovery authority. The primary
failure remains selected through cleanup. Events without failure metadata keep
their canonical bytes.

Native recovery inventory excludes only pinned-layout, runner-owned mode-0600,
single-link `<20-digit-offset>.stdb.ofs` files in the sole replica's `clog`
directory when the same offset has a supported nonempty `.stdb.log`. The full
source seal still includes the offset index. It is an optional upstream seek
index, not selected recovery data. Orphan, malformed, unknown or unsafe entries
retain the existing refusal behavior; log-tail and snapshot selection limits
are unchanged.

Native evidence binds the exact limits issued to its reader. Live output must
match that request. Revalidation of opaque evidence keeps those same limits;
protected retained records declare globally bounded limits and still undergo
strict schema, usage and canonical identity checks. Global maxima are caps, not
a substitute request. Parsing retained bytes grants no source, owner, capture,
publication or recovery authority; the existing protected hashes and scope joins
remain required.

Initial module publication records `unit_absent` in its protected target proof.
The source is an unpublished allocation, and ordinary deployment installs the
managed engine service after publication initializes it. Publication observes
actual service-file absence and the unchanged canonical future unit hash. It
refuses any installed file, including a matching file, and unsafe or unknown
paths. Initialized publication still requires the exact installed service.
Activation, completion and transition recovery require the same unit observation
as the retained target proof. Existing process, job, container, reader, ingress,
fence and tree checks remain required; unit absence grants no start authority.

Control schema 28 admits the two protected final publication proof kinds,
`transition_target` and `transition_completion`, alongside retained proof kinds.
The normal store opener upgrades schema 27 atomically, preserving every proof
key, kind, hash and canonical byte. Read-only openers refuse a schema that needs
migration. The typed proof-kind catalog supplies the current SQL constraint;
unknown kinds, foreign claims, duplicate proof keys, invalid hash lengths,
oversized records, updates and deletes remain refused.

Incoming publication and live-restore transitions compare the initialized
repository destination through `nativeArchiveLocation`, the same constructor
used by repository initialization. The archive prefix retains its ownership
trailing slash; the transport location omits that delimiter before hashing.
Binding, incoming epoch, exact destination and original creation grant still
must match. A different prefix, bucket, endpoint, region or path mode refuses
transition admission.

Current-module extraction for online capture and initialized exact preparation
binds the current committed engine permit under its held preparation guard.
It verifies the exact current Wasm and native program identities before staging,
then inspects the pinned runtime image and checks the extracted current schema.
The inherited backup policy keeps its original deployment-plan identity and
cannot select the current extraction epoch. Candidate publication extraction
continues to require its reviewed deployment plan; current-source extraction
grants no candidate publication or writer start authority.

Confirmed recovery-pair observation uses one private authenticated v1JSON
`SubscribeMulti` initial snapshot with `confirmed=true`. The pinned engine
computes all three independent table queries under one read transaction and
waits for that transaction offset to become durable before delivery. Separate
SQL reads cannot supply this snapshot while scheduled writers advance.

The observer requires the exact protocol, identity frame, request/query ID,
complete named row shape from the admitted registered base/default-column
layout, table IDs/names, row counts and fixed schema marker. Pinned empty-table
omission means empty rows; a missing populated side still refuses as an orphan.
No retry, join, intersection or truncation repairs inconsistent data. New
observations record `confirmed_subscription_lower_bound`; retained historical
SQL records keep their original basis and canonical bytes.

Native capture failures after upload carry a fixed closed code for remote
verification, source recheck, live engine recheck, module recheck or verified
point commit. Post-verification checks run in that order and stop at the first
refusal. Codes contain no subprocess output, row, credential, path or identifier;
underlying errors remain wrapped for internal cause/cancellation handling.
Unknown upload results and retained workspaces keep their existing recovery
requirements. Original backup resolution uses the same verification and commit
codes while adopting retained evidence without another upload.
