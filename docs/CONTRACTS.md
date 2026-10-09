# Brine v1 contracts

**Status: Approved design target, not an implemented API.** See [DECISIONS.md](DECISIONS.md) for where state lives, transport, Caddy, secrets and ports. The CLI exposes `version`, `doctor`, the placeholder `tui`, `validate`, and offline `plan`; connected planning and deployment remain unimplemented. Stabilize these contracts with tests before claiming compatibility.

## Command surface

~~~text
brine init [directory]
brine validate ./brine.toml --policy ./policy.toml
brine plan ./brine.toml --target staging
brine plan ./brine.toml --offline --snapshot ./snapshot.json --policy ./policy.toml [--state ./brine-state.json] [--out ./plans]
brine apply PLAN_ID --target staging
brine status --target staging
brine status --operation OPERATION_ID --target staging
brine logs APP --target staging --tail 100
brine rollback APP --release RELEASE_ID --target staging
brine config set APP KEY=VALUE --target staging        # env, resources, domains, health
brine secret set APP NAME --target staging             # value from stdin; stores a new immutable version, unbound until applied
brine restart|stop|start APP --target staging
brine remove APP --target staging                      # archives data under an archive ID for 30 days (D8)
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

Plan is read-only with respect to runtime/proxy/data; it stores the plan in the target's control database (D1). `plan --offline` compares to a supplied snapshot and yields a **non-applyable** preview. Applying always refers to a recorded immutable plan and revalidates target identity, policy, observed generation, and artifact hashes. Every mutating command above (`config set`, `restart`/`stop`/`start`, `rollback`, `remove`, `data purge`, `restore live` and the `host` verbs) returns a **plan**, and nothing changes until `brine apply PLAN_ID` (D8). `--apply` may combine the two for agents when policy allows. It still records the plan and journals the operation. The one exception is `secret set`, which stores an immutable new version that stays unreferenced (D5) until a plan binds it, so it never changes a running app on its own. `remove` archives app data rather than deleting it; permanent deletion is the separate, policy-gated `data purge`.

### Local validation and offline planning (P01-04)

`validate` and `plan --offline` read only local regular files. Neither performs network, SSH, subprocess, runtime, proxy or control-database operations. Both require exactly one app path and a nonempty explicit `--policy`. Specs and policies are bounded to 1 MiB each; snapshots and committed-state files to 16 MiB each. Invalid arguments, unreadable inputs, invalid specs, malformed snapshots and malformed committed state return `invalid_usage` with exit 2. Policy decode or enforcement refusals return `policy_refused` with exit 4. Diagnostics never echo input values or paths.

`validate` human output summarizes the normalized name, digest-pinned image, domains, container port, policy version and environment/secret counts. Its machine `data` is the normalized desired configuration, with `environment` replaced by a sorted array of names, never literal values. Health/resource defaults, policy hash/version, app port range and secret references remain visible.

`plan` without enabled `--offline` refuses with `offline_required` and exit 2 before reading files. A supplied snapshot is never treated as a connected host. The image digest comes from the validated app; an installed app's matching digest retains its observed platform. Otherwise Linux and the snapshot architecture are explicit offline assumptions, not verified registry metadata. No manifest is fetched. The selected platform manifest observation is always `unknown`, even when an installed app supplies the platform.

`--state` reads the separate `plan.BrineState` JSON schema, including every normalized previous release input. Its decoder rejects unknown, missing, duplicate, case-aliased and null fields, as well as trailing JSON. Without `--state`, an affirmatively empty, generation-zero snapshot permits an empty bound state for a fresh-host preview. Other snapshots receive unbound empty state and produce a conflict; the CLI never invents installed release history from the new desired app. Use the example committed-state file for the installed-app no-op case.

Create, update, no-op and conflict are all successful read-only planning results, with exit 0 and `ok: true`. A conflict has `data.kind: "conflict"` and diagnostics, not executable changes. This distinguishes a completed analysis that found obstacles from a failed invocation or the category-5 refusal to reuse corrupt stored evidence. Callers must inspect `kind`, not infer deployment readiness from exit 0.

Machine plan `data` is exactly `planview.JSON`'s redacted projection, including its hash and diff, never the `planfile.Offline` envelope or retained literal environment values. `--out` does not change that projection. Human output prominently begins with `OFFLINE PLAN — NOT APPLYABLE`, renders the planview diff, and prints the complete plan hash plus a quoted file path when written.

Only `plan --offline --out <existing directory>` persists anything. It writes one private, hash-named offline plan through `planfile.Write`, without creating a directory or control database. Temporary files used for atomic publication are removed. Creation time and tool version remain outside the fingerprint. Sequential retries verify and reuse an existing hash-named, regular 0600 file without rewriting its original metadata; malformed, tampered, non-private or nonregular existing evidence refuses with `conflict` and exit 5. Other output I/O failures use exit 1. The file retains literal settings for fingerprint verification and is not a log or safe public presentation. It never grants apply authority.

## Machine modes

`--json` emits exactly one newline-terminated JSON object on stdout for each invocation, including command and flag errors. Stdout must be writable for this guarantee. Human help requested with a machine flag is returned as a string in `data.help`. Without a machine flag, commands retain human output.

`--jsonl` emits one compact JSON envelope per line. One-shot commands other than `logs` emit exactly one line containing the final result, identical to `--json`. `logs` emits one `log` envelope per entry, then a `complete` invocation result (including an empty tail). Future streaming commands may emit progress envelopes followed by one final invocation result. Every line uses the version 1 envelope below, with event payloads inside `data`; there is no surrounding array, pretty printing, blank line or unstructured status text. This foundation defines framing, not future event names or payload schemas. A final accepted result does not mean the background operation has finished.

`--json` and `--jsonl` together are a usage error with exit 2 and one `invalid_usage` JSON envelope on stdout, even when help is requested. An explicit `--json=false` or `--jsonl=false` disables that flag; the last value of each flag wins. Flags after `--` are positional arguments. Malformed machine-flag values still get an error envelope, even when a later false value disables that flag. Machine flags are recognized before parsing so an earlier unknown flag cannot suppress the envelope.

`__complete` and `__completeNoDesc` use Cobra's shell-completion protocol. Their arguments describe another command line, so machine flags inside that command line do not change the protocol response.

`--no-input` forbids all prompts. JSON and JSONL also forbid prompts without requiring `--no-input`. Read-only commands that need no input still succeed. Commands must route every prompt through `internal/cli.RequestInput`, which checks the flags and injected input/output streams before running the prompt callback. If input is needed but forbidden, or either stream is not a terminal, it returns typed `input_required` with exit 2 before printing or reading anything. Callers must supply explicit arguments instead; no automatic answer or authorization is implied. Allowed prompt callbacks receive the injected context, stdin and stderr. The helper checks cancellation before asking. No production command prompts yet.

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
| `logs_truncated` | 1 | false | The journal output was truncated; request a smaller tail. |
| `logs_limit_exceeded` | 1 | false | The log response exceeds its bounded tail or byte limit; request a smaller tail. |
| `logs_invalid_journal` | 1 | false | The journal response is malformed. |
| `internal_error` | 1 | false | The operation failed. |
| `invalid_usage` | 2 | false | Invalid command or arguments. Use --help for usage. |
| `dependency_missing` | 3 | false | A required dependency is missing or incompatible. |
| `policy_refused` | 4 | false | The operation was refused by policy. |
| `conflict` | 5 | true | The plan is stale or another operation holds the lock. |
| `recovery_required` | 6 | false | Manual recovery is required before continuing. |
| `interrupted` | 130 | false | The client was interrupted. |
| `tui_interactive` | 2 | false | tui is interactive; remove --json, --jsonl and --no-input |
| `tui_terminal_required` | 2 | false | tui requires terminal input and output. |
| `offline_required` | 2 | false | Connected planning is not available; use --offline with --snapshot and --policy. |
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

`brine logs APP --target NAME --tail N [--since RFC3339]` reads the enrolled target configuration from the same client directory as `enroll`. `--config-dir` overrides that directory. Tail defaults to 100 and must be between 1 and 1000. `--since` accepts only an RFC3339 timestamp with fixed-width date/time fields, a dot for fractional seconds (up to nanosecond precision), and timezone hours/minutes in range. The runner parses it with `time.RFC3339` and passes normalized UTC to journalctl, not the original expression. Journal expressions, paths and extra options are refused.

The read-only dispatcher operation is `logs`, with exact-case `args` fields `app`, `tail` and optional `since`. The runner checks inventory before running journalctl. Apps with unknown ownership, no matching container unit or ambiguous matching units return `logs_ownership_refused`. Only the inventory-recorded `<app>.container` or legacy `brine-<app>.container` is accepted, and its service name is derived internally. A request cannot supply a raw service name. Ownership follows the existing inventory's marked-rendered-unit or Brine-prefix contract; an arbitrary user service is not readable.

The runner executes typed arguments equivalent to `journalctl --user -u <derived-unit> -n <tail> -o json --no-pager --all`, with an optional separate `--since` argument and a ten-second deadline covering collection and reading. It does not switch users, mutate state or invoke a shell. Subprocess capture is bounded to 256 KiB by the shared executor. Raw journal bytes and the serialized sanitized line array must each fit 128 KiB. `--all` prevents journalctl from silently omitting large fields. Extra records or byte overflow fail closed without partial logs. Capture truncation has the explicit `logs_truncated` failure marker, not successful partial entries. Malformed journal fields, including missing or null messages, fail closed as `logs_invalid_journal`. No raw stdout, stderr, journal metadata or underlying error details are presented.

The host response and client `--json` result contain an array of `{timestamp, priority, message}`. Timestamps are UTC RFC3339 with available microseconds; priorities are integers 0-7. Strings and journalctl's binary-message byte arrays are supported. An empty tail is `[]`. Human output renders one timestamped line per entry and escapes embedded newlines. ANSI escapes and control characters are stripped from messages before presentation.

`--jsonl` uses version 1 envelopes with `command: "brine logs"`. Each entry has `data: {"event":"log","line":{...}}`. The final result has `data: {"event":"complete","count":N}`. There are N entry lines plus one final invocation-result line, including one completion line for an empty tail. Validation or operational failure produces one failure envelope and no partial entry stream. The client buffers this bounded response before publishing it; this is not a live follow stream.

Redaction is **best-effort**, not a promise that unknown secret values can be identified. Brine never retrieves plaintext Podman secret values to build a redaction list. Quoted values are matched with escape awareness, including escaped quotes and backslashes. Patterns cover secret-looking assignment/JSON keys, bearer credentials, URL passwords, token-like strings of at least 32 characters, and private-key blocks (including blocks spanning returned entries). The client validates and sanitizes structured messages again. False positives are intentional. Arbitrary prose secrets, unusual encodings, or private-key tails whose opening marker is outside the requested window may evade these patterns. Applications must not log secrets.

The Debian 13 JSON fixture was captured read-only on the authorized test host and scrubbed. The operator's existing user-unit queries returned no entries, so the recorded nonempty shape is a system journal record. Binary-message parsing uses a synthetic byte-array fixture. This evidence validates journal serialization, not a deployed app or end-to-end access through an updated remote dispatcher.

## Restricted dispatcher and client transport (first half of P02-02)

`brine host serve` is hidden and always speaks JSON, independent of presentation flags. Command resolution selects dispatcher setup before interpreting presentation flags, including leading global flags such as `brine --json host serve`. Every argument after `serve` is ignored. It refuses effective UID 0 before reading stdin. It clears the process environment and records only the byte length of `SSH_ORIGINAL_COMMAND` on stderr. That variable never selects an operation or contributes response text. A pipe read deadline limits incomplete requests to five seconds. Regular files, which do not support read deadlines, are read through a bounded buffer with a five-second timer and cancellation signal. Unsupported deadlines on other input types are still refused. Processing has a fifteen-second context deadline; inventory implementations must honor cancellation. Enrollment must still enforce D7. Clearing the environment inside a binary cannot undo code run by a login shell or dynamic loader before it starts.

The dispatcher reads stdin through EOF, with a **64 KiB** bound including whitespace. It accepts exactly one UTF-8 JSON object with these required, exact-case fields:

~~~json
{"schema_version":1,"op":"ping","request_id":"example-1","args":{}}
~~~

Duplicate or unknown fields, case aliases, null fields, wrong types, missing fields, trailing data and a second request are refused before dispatch. `request_id` is caller-supplied correlation input, limited to 1-128 ASCII letters, digits, hyphens and underscores. It does not grant authority or provide idempotency. The result envelope is unchanged and does not echo this ID. Each SSH call carries one request and its one response.

The immutable operation registry declares `ping`, `inventory` and `logs` as `read_only`. `ping` and `inventory` have separate typed empty argument decoders. Both require `args: {}`. `ping` returns `data: {"server_version":"<binary version>","protocol_versions":[1]}`. `inventory` calls `dispatch.Inventory.Collect(context.Context) (target.Snapshot, error)` and returns the validated snapshot. Without that provider it returns `dependency_missing`; live collection is a separate task. No mutating operation is enabled. A future mutating entry remains refused until operator policy and durable operation state are implemented.

Responses use the existing `result.Envelope`, with `command` equal to `brine host ping`, `brine host inventory` or `brine host logs`. Pre-dispatch refusals use `brine host serve`. They have a **1 MiB** bound including the terminal newline. These host operations are not streams. `logs` JSONL framing is a client presentation of the single bounded host response. Future streaming operations must declare and test JSONL explicitly; callers cannot request a stream through argv. Wrong request schemas exit 3, unknown operations exit 4, malformed or oversized input exits 2, and root invocation exits 4. Other failures use the existing categories above.

`transport.LoadTarget` reads a client-side file with a **16 KiB** bound. Its required fields are `name`, `destination`, `identity_path` and `pinned_host_key`; decoding rejects unknown fields, aliases, duplicates, nulls and trailing data. Names are lowercase ASCII labels. Destinations are Host aliases or explicit `user@host` values with an ASCII username. Host and alias values contain ASCII letters, digits, underscores, dots and hyphens, without a leading hyphen, URI scheme or inline SSH options. An explicit root username is refused; the server independently refuses effective UID 0 even when an alias supplies the user. Numeric dotted host labels are accepted. Custom ports and proxy hops come from the trusted user SSH configuration, not request fields. Identity paths are clean absolute Unix paths containing only ASCII letters, digits, underscores, dots, slashes and hyphens. The initial pin format is one `ssh-ed25519 <base64>` public key without a comment, with its SSH binary structure validated. Inline IPv6 literals, other host-key algorithms and paths containing whitespace or SSH expansion tokens are not accepted in this initial transport. Host aliases can resolve IPv6 destinations and custom ports through SSH configuration.

The caller supplies an existing or newly created private `KnownHostsDir` outside the repository. Each target has a mode-0600 `<name>.known_hosts` file in a mode-0700 directory. The pin uses the fixed host-key alias `brine-pin`. Existing pins must match byte-for-byte; they are never replaced automatically. Final-component symlinks and unsafe file modes are refused. Local configuration, its parent directories and identity files are trusted operator-owned input, not a sandbox against another process running as the same client user.

The client selects the system `ssh` from `/usr/bin:/bin`, then uses `localexec` with separate arguments, request bytes on stdin, a fresh account-derived environment and separately bounded stdout/stderr. Only the explicitly selected `SSH_AUTH_SOCK` is forwarded for agent support; ambient PATH, runtime, configuration and loader overrides are not inherited. D1's trusted client SSH configuration, Host aliases, configured ports, proxy hops and agent support are preserved. Command-line safety options take precedence over scalar configuration values: `BatchMode=yes`, `StrictHostKeyChecking=yes`, the pinned `UserKnownHostsFile`, `GlobalKnownHostsFile=none`, `ForwardAgent=no`, `ClearAllForwardings=yes`, `IdentitiesOnly=yes`, `RequestTTY=no` and `PermitLocalCommand=no`. The explicit identity is supplied with `-i`; `--` precedes the destination. DNS host-key trust and key updates are disabled. OpenSSH `IdentityFile` entries are additive, so an explicit identity plus `IdentitiesOnly=yes` is not an exclusive identity allowlist. Configured identities may also use the agent. The server's restricted key and D7 protections, not the client's configuration, establish the remote authorization boundary.

Connection establishment is limited to five seconds, keepalive failure to one five-second interval, and the complete call to fifteen seconds or the caller's earlier deadline. Stdout and stderr each have a **1 MiB** bound; any overflow fails the call, even when a valid response prefix exists. Raw diagnostics are not presented. Responses are decoded strictly at every declared nesting level, including inventory snapshots, and the error code, fixed message, retryability and SSH exit category must agree. Exit 255 is transport failure. Calls never retry automatically. A timeout or lost connection is not proof of a remote mutation's outcome.

Operator enrollment and live restricted-key/PAM/startup-file verification are described below. The initial protocol and fake-SSH tests alone do not establish the full P06-01 production boundary.

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

A plan binds desired spec hash, target identity, observed generation, image index digest, selected platform manifest observation and platform, policy version, secret-reference versions, artifact digests, expiry and change summary. Identical canonical inputs should generate identical hashes; timestamps live outside hash material. Plan acceptance is not a general authorization grant.

### Pure planner contract, schema 1

`internal/plan.Build(Input)` accepts `policy.Desired`, `target.Snapshot`, caller-supplied pinned image metadata and `plan.BrineState`. It performs no I/O and reads no clock. The returned plan owns its memory. Treat it as immutable after hashing. Expiry, creation time, approval, transport provenance and offline/not-applyable status belong in a caller-owned envelope, never inside the hashed plan. Offline provenance must remain explicitly not applyable when P01-04 and P01-05 add their presentations and files.

`target.Snapshot` is the single inventory contract. `LiveCaddyFiles` records the domains actually served by each root or imported file, including files outside the Brine generation. Each file has a stable opaque identifier, an observed app association and an explicit domains observation. `PortOwners` records app associations, process names and systemd units for bound listeners. Empty app associations mean unrelated or unattributed software, never ownership permission. A used existing app port without affirmative ownership conflicts. Domain ownership is never guessed from a filename. Known empty arrays are affirmative observations, not defaults; unknown required observations conflict.

Committed release state is separate from inventory. `plan.BrineState` comes from the target control database under D1 and binds its target identity and generation to the snapshot. A mismatch produces a `stale_brine_state` conflict. Its non-null `Releases` array contains `CurrentRelease` records with the release ID, previous normalized desired configuration, expected image, stable host port, immutable secret bindings and committed Quadlet/Caddy artifact digests. A known empty release array means no committed releases. Inventory app records no longer contain current release IDs. Missing state is not fabricated from new desired inputs.

A secret-only app inventory record does not grant authority to replace a Caddy file. Replacing an existing namesake generation file requires a matching committed Caddy artifact record. The live app association, served domains, generation file hash and committed artifact must agree; otherwise the planner emits a conflict with no changes. This includes drift between the selected files on disk and the config actually served by Caddy.

Observed addresses are normalized at the target boundary. Plain hostnames, HTTP(S) authorities and optional numeric ports compare by lowercase hostname without a terminal DNS dot. IPv4, IPv6, wildcard hostnames and catch-all listeners remain explicit claims. Schemes and ports do not exempt a hostname from ownership conflicts. Credential-bearing URLs, paths, queries, fragments and invalid hostnames are rejected. Duplicate canonical domains in a file are rejected. Wildcard claims cover their immediate subdomains; catch-all claims cover every desired domain.

Deployment requires known systemd 257, Podman 5.4, Caddy 2.6 and a packaged passt revision, known-true cgroup v2 and lingering, and a known non-root runner user. Distribution revision suffixes are accepted within those pinned version lines. Passt uses the Debian `0.0~git<date>.<revision>` package-version family, with optional distribution suffixes. Missing required runtimes, unknown readiness, unsupported observations or incompatible versions/capabilities produce conflicts with no changes. Litestream remains optional for the v1 app spec, which requests no persistence or backup behavior.

The planner uses `plan.Image`, separate from inventory's `target.Image`. Its fields serialize in order `digest`, `platform`, `manifest_digest`. `digest` names the exact index digest pinned in the desired image, or the single-image digest when no index exists. `platform` records OS and architecture. `manifest_digest` is an explicit `target.Observation[string]` for the selected platform manifest. Its only accepted forms are `{"status":"known","value":"sha256:<64 lowercase hex digits>"}` and `{"status":"unknown"}`. Missing status, absent/unsupported status, a known observation without a valid digest, and an unknown observation with a value are input errors. The same shape and validation apply to committed release images, pull-image changes and image diffs. Unknown is valid preview evidence, never apply authority. The pure planner never fetches a manifest or infers its digest from the index digest. For a single-image pin, a verified manifest digest can equal the pin. Missing metadata or a different digest is an input error, not permission to resolve an image online. A platform mismatch produces a conflict. Secrets bind to the highest positive numeric version with the exact name `brine-<app>-<ref>-v<n>` in that app's inventory. Bindings include the environment name, logical reference, immutable Podman name and Podman ID. No secret values are accepted or resolved. Precreated secrets can live in an app inventory record with no committed release in `BrineState`.

Port allocation keeps an existing app port, even when a later policy range changes. Otherwise it selects the lowest policy-range port absent from listeners, used ports and all app reservations. No-op requires a matching committed configuration fingerprint, the expected image, the app's container unit and Caddy file, and unchanged recorded artifact hashes. The previous configuration fingerprint is computed from the committed release, never from the new desired input. A secret-only record with no committed release permits creation only when it claims no unrelated runtime or Caddy artifacts. Recorded artifact drift conflicts.

Changes have typed payloads in this order: allocate a port if needed, pull and verify the image, bind existing secret versions in environment-name order, render Quadlet, stage the complete next Caddy generation, restart the app. The Caddy payload preserves every unrelated file and replaces only the owned app file. Before staging any whole-generation change, the planner checks every committed Caddy artifact against the selected generation and live file observations, including apps not being changed. A missing file, changed hash, wrong live app association or changed served domains produces an artifact-drift conflict with no changes. Matching committed files and unmanaged files remain in the preserved set. Staging is intent, not publication. Later apply phases own whole-config validation, quiescing, health checks, activation, publication and rollback. A conflict has no changes.

Fingerprints use lowercase `sha256:<hex>` over compact Go `encoding/json` output. There are no maps in hash material. Set fields sort on defensive copies. Desired domains sort lexically; desired environment and secret entries sort by environment name. Snapshot apps, units, secrets and Caddy files sort by name; used ports sort numerically. Live Caddy files sort by identifier and their normalized domains sort lexically. Port owners sort by port, app, process and unit. Brine releases sort by app; previous desired sets use the desired sort order, units sort by name and secret bindings sort by environment name. Diagnostics sort by code then field and duplicates collapse. Changes retain execution order.

- `DesiredHash` hashes exactly `policy.Desired.CanonicalBytes()`, without a trailing newline. This includes the normalized app, policy version, policy hash and port range.
- `ConfigHash` hashes the JSON object with fields in order `desired`, `image`, `port`, `secrets`. `desired` is the canonical desired JSON object, not an encoded string. The other fields contain the expected image including the selected platform manifest observation, selected host port and ordered immutable secret bindings. The same computation over `CurrentRelease.Desired`, image, host port and secret bindings reconstructs the previous committed fingerprint for no-op comparison.
- `Hash` hashes the JSON object with fields in order `desired`, `snapshot`, `brine_state`, `plan`. The first three are canonical JSON objects. `brine_state` includes its target binding, generation and every committed release field, including previous normalized configuration and artifact digests. `snapshot` is the complete `target.Encode` result with its terminal newline removed by JSON compaction. This conservatively includes all observed facts, including disk and dependency observations, rather than trying to predict which future apply checks need them. `plan` is the complete output, including schema, kind, app, target identity and host-key fingerprint, observed generation/status, policy version/hash, desired/config hashes, expected image/platform and selected platform manifest observation, host port, secret bindings, ordered changes, the optional typed `diff` and diagnostics, with its `hash` field set to the empty string. The Quadlet desired payload has an empty `environment` array and a separate sorted `environment_keys` array; literal environment values appear in the transient input hash material but never in the returned plan. Changing a literal still changes the desired/config/plan hashes. The final output replaces that empty field with the computed fingerprint. Canonical plan serialization adds one terminal newline, which is not hash material. Both `ConfigHash` and `Hash` bind the manifest observation status and, when known, its full digest. Changing the manifest digest or changing unknown to known changes both hashes. Observation pointer identity is not hash material.

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

Under the host mutation lock, connected apply must refuse a plan whose selected platform manifest digest is `unknown`, before any runtime mutation. A connected planner must resolve and verify the index digest, selected platform and manifest digest, then record a new immutable plan. Apply must verify both digests and platform against the runtime or registry evidence. `podman inspect` reporting the index digest alone does not verify the selected platform manifest. Apply must not fill in an unknown digest or silently rehash an existing plan. Offline envelopes remain non-applyable even if a caller supplied cached known manifest metadata. This is an acceptance rule for P03-02, not an implemented apply endpoint.

### Offline plan files, schema 1

`internal/planfile.New(plan.Input, Metadata)` computes an offline plan without I/O or a clock. The opaque `Offline` type owns its bytes and exposes constant `Applyable() == false` and `Reason() == "offline"`. It has no approval transition, writable provenance fields or conversion to apply authority. `Plan()` returns a defensive copy of the pure intent, not an authorization. The zero value cannot be written.

The JSON envelope contains `schema_version`, canonical `plan`, `hash`, `applyable: false`, `reason: "offline"`, `snapshot_identity`, `snapshot_generation`, `inputs` and `metadata`. `inputs` retains the supplied normalized desired configuration, complete snapshot and caller-supplied committed release state so readers can rerun `plan.Build` and verify its existing hash contract. This is offline evidence, not a client control database. The envelope does not change the planner fingerprint. `metadata` contains required caller-supplied `created_at` and `tool_version`, outside that fingerprint. Creation times serialize in UTC. A different timestamp or tool version preserves the plan hash but makes different file content, so a same-name rewrite refuses it rather than discarding either creation record.

`Read` and `Write` require caller-controlled paths and directories. They are not hardened against an attacker replacing directory entries during pathname-based operations. `Write` requires an existing directory and uses `<lowercase hash hex>.plan.json`. It writes a mode-0600 temporary file in that directory, fsyncs and closes it, then publishes atomically without replacing an existing name. Linux uses `renameat2(RENAME_NOREPLACE)`; other platforms use a same-directory hard link and remove the temporary name. The directory is fsynced after publication. Identical bytes in an existing regular mode-0600 file are idempotent. Different bytes, symlinks or unsafe file modes produce `ConflictError`. Prepublication failures remove the temporary file. A directory-sync error after publication leaves a complete file and returns an error; an identical retry is safe. Unsupported no-replace publication fails closed rather than falling back to overwrite-capable rename.

`New`, `Decode`, `Write` and `Read` share the 16 MiB limit on the exact bytes persisted, including the terminal newline. `Decode` checks both input size and canonical output size because JSON escaping can expand content. It rejects duplicate keys, unknown fields, noncanonical field-name casing at every nesting level, trailing JSON, unknown envelope or plan schemas, absent or forged offline markers and missing creation metadata. It recomputes the plan from the retained inputs and compares the complete canonical plan, hash and snapshot bindings. `Read` also verifies the filename. Corrupted content returns `IntegrityError`; unknown versions return `SchemaError`; malformed input returns a fixed `DecodeError` that does not echo values. The schema accepts secret references and immutable version IDs, never a secret-value field. Literal environment settings remain governed by the existing spec and policy contract; the file layer does not guess whether arbitrary text is a credential. No file checksum authenticates creation metadata, and these files never grant permission to apply.

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

`restore test` writes into a new isolated directory or disposable fixture, checks `PRAGMA integrity_check`, verifies expected application invariants and reports recovery-point information if available. It **never overwrites the production DB**. Until P04-09 ships, live restore stays an explicitly approved operator runbook; after that it is the policy-gated `restore live` plan (D8). Database storage or backup deletion is never part of image cleanup.

## Source references

The upstream manuals for [Quadlet](https://docs.podman.io/en/stable/markdown/podman-systemd.unit.5.html), [Caddy admin API](https://caddyserver.com/docs/api), [Litestream R2](https://litestream.io/guides/s3-compatible/) and [Litestream caveats](https://litestream.io/tips/) are useful starting points; implementation must pin and test actual installed versions.

## Operator enrollment

`brine enroll SSH_DESTINATION --target-name NAME --deploy-key /absolute/deploy.pub` uses the operator's existing OpenSSH configuration and strict known-host checking. It never uses the restricted deploy key for administration. Root SSH or non-interactive sudo is required. OpenSSH bootstrap uses fixed shell command strings with a strictly validated temporary-directory grammar; it trusts the already-authorized operator account that owns that temporary copy. It is not a root-owned staging area or an independent copy-to-exec content attestation. A differently sized or non-Linux client must supply a matching amd64/arm64 Linux executable with `--host-binary`. The private deploy key defaults to the public key path without `.pub`; `--identity` overrides that path. Only canonical Ed25519 deploy keys are accepted.

Enrollment refuses non-terminal stdin, `--no-input` and machine-output modes. `--yes` does not skip confirmation. The operator must type the target name after the complete change list. The matching binary runs read-only inventory before that prompt. Debian 13, cgroup v2 and the D2 runtime versions are required. Unknown listener, SSH, PAM or package state fails closed. The first non-comment main SSH directive must be the unconditional Debian `Include /etc/ssh/sshd_config.d/*.conf`, with no earlier-sorting drop-in. Another proxy on ports 80/443, preexisting unowned runner resources and other Caddy file imports are conflicts. A journaled root-owned runner-only SSH drop-in forces the dispatcher for every runner login and enforces `/etc/ssh/brine/authorized_keys/%u`, no authorized-key command or principals file, and no PTY or forwarding/tunnels. sshd evaluates later Match/Include syntax itself. Effective global `PermitUserEnvironment` must already be exactly `no` across IPv4/IPv6 loopback and remote connection specifications; enrollment never changes that global-only directive. Effective AcceptEnv may contain only LANG, LC_* and the exact display-only names COLORTERM and NO_COLOR; no additional wildcards or display-name prefixes/suffixes are accepted. SetEnv may set only concrete locale variables. Loader/shell/path/runtime variables and other patterns are refused before mutation. A bounded all-branch Include audit supplements the four effective probes because environment lists are additive; this audit conservatively refuses unsafe entries even for other users. Include accepts only literal absolute paths or the exact `/etc/ssh/sshd_config.d/*.conf` pattern, excluding dotfiles and sorting in the controlled C locale. Other patterns, character classes, `?`, braces, tilde, relative paths, ambiguous escapes and any Include inside an included file are refused. Read-only preflight and staged validation compare the complete audited unique source load order with bounded `sshd -ddd -T` debug output; unavailable, truncated or mismatched traces refuse enrollment. The complete staged SSH configuration passes `sshd -t` and all forced-setting probes before promotion and SSH reload. Known failure restores verified original state and reloads; interrupted/unknown reloads keep a durable pending marker and require explicit operator undo rather than automatic apply retry. Undo validates and reloads the restored configuration before removing protected key resources.

The operator-only hidden `host enrollment` endpoint accepts bounded JSON on stdin. It is not in the deploy dispatcher's operation allowlist. Its root-owned journal under `/var/lib/brine-enrollment` records intent before each mutation, runner identity, directory identity, file hashes, original file bytes/metadata, package versions and original Caddy service state. Reruns check observed state before applying a step. Directory intents explicitly remain pending until their inode has been durably recorded and ownership/mode changes completed. Interrupted creation is reconciled only for an empty directory under protected parents with the expected identity and root/bootstrap or planned ownership; unknown contents refuse recovery. Journaled original file bytes and metadata are a valid incomplete replacement state. A different binary or key is not an upgrade operation and cannot silently replace an enrolled one.

Missing packages use a simulated apt transaction with exact versions for every new dependency. Missing runtime candidates are checked against D2 before confirmation. Apply is bound to the displayed fixed change list, exact transaction and relevant inventory; it repeats the binding check before creating a journal and under the lock before performing enrollment changes. Changed candidates, dependencies, installed versions or host trust require a fresh inventory and confirmation. Enrollment refuses package upgrades, removals and transaction drift. It masks Caddy before installation, removes a new Caddy package's verified default site from the candidate root configuration, validates the Brine root and only then unmasks and starts the service. Existing root Caddy configuration keeps its sites and gains the one fixed import. The complete candidate is journaled and validated in the protected enrollment directory before live replacement. Known validation/promotion failures restore verified original bytes and metadata; unknown or changed observed state keeps the journal and fails closed. Interrupted promotion resumes from either the recorded original or intended file, and candidate cleanup is recorded. Package removal during undo is limited to recorded installed packages; it does not autoremove or purge preexisting package configuration.

The runner has a root-owned home and `.ssh`, with only runner-owned `.config`, `.local`, `.cache` and state below them. `authorized_keys` names `/usr/local/bin/brine host serve` with `restrict`. The root-owned external deploy key is at `/etc/ssh/brine/authorized_keys/brine`; `.ssh` remains empty after verification. A root-written inventory identity key at `/etc/ssh/brine/inventory-key` supplies the shared snapshot collector without trusting runner-writable state. Caddy's generation parent is root-owned and group-writable by the runner so the unprivileged generation adapter can create and promote generations. The polkit rule grants only `caddy.service` reload.

Before writing the successful client target, enrollment calls `ping` with the actual private deploy key through the pinned transport, repeats it with a shell command that must be ignored, and checks that a root-planted `.bashrc` did not execute. It also calls inventory through the restricted key, then removes the planted startup file and unrestricted home `.ssh/authorized_keys`, `.ssh/authorized_keys2` and `.ssh/environment` fixtures and repeats the host inventory. Podman’s read-only resource queries can create or update empty SQLite/lock metadata. Verification checks that containers, images, volumes and secrets are empty, accepts only the fixed metadata tree with runner ownership and no symlinks/hard links, and records its hashes and modes in the protected journal. Operator-confirmed verification may refresh that empty metadata record after repeating all checks; undo never refreshes it and refuses hash drift or any unknown/application data. Target files live in the private client configuration directory; no key or inventory goes into source control.

`brine enroll SSH_DESTINATION --target-name NAME --undo` requires the same interactive confirmation. Partial enrollment can be undone through authenticated operator SSH even when final client configuration was not written. A read-only undo-specific probe checks host trust and journal-associated runner ownership without requiring deploy SSH environment prerequisites; a subsequently unsafe AcceptEnv policy must not prevent protected operator cleanup. Undo verifies recorded hashes and directory identity, refuses app data or changed Caddy generations, restores the original root Caddy configuration and service state, stops the runner and waits for its processes/runtime directory to disappear. It reconciles actual account absence after `userdel --remove`, including exit 12, instead of treating the exit code as proof or retrying blindly. Only a verified Brine-created home is removed. The runner account/group, linger, runtime directory, subordinate IDs and processes must all be absent before success. Preinstalled packages are never removed.
