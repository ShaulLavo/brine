# Configuration reference

Brine reads three kinds of configuration: the app definition (`brine.toml`), the operator policy, and the client target directory. Both TOML files are strict. Unknown fields, duplicate keys, wrong types and key names with different case are refused. Each file is limited to 1 MiB. Source: `internal/spec/spec.go`, `internal/spec/data.go`, `internal/policy/policy.go`.

Validate an app against a policy with `brine validate brine.toml --policy policy.toml`. Samples are in `examples/offline/`.

## App definition (`brine.toml`)

One file describes one app. `schema_version` is the only supported version, `1`.

### Top-level keys

| Key | Type | Required | Rule |
| --- | --- | --- | --- |
| `schema_version` | integer | yes | Must be `1`. |
| `name` | string | yes | Lowercase DNS label, 1 to 63 bytes. |
| `image` | string | yes, unless engine-only | Explicit registry and repository, pinned with `@sha256:` and a 64-hex digest. A tag is allowed before the digest. |
| `container_port` | integer | yes, unless engine-only | 1024 to 65535. |
| `domains` | array of strings | yes | At least one. ASCII DNS names with two or more labels. No IP addresses, no `xn--` labels, no duplicates. Folded to lowercase. |
| `health` | table | no | See [`[health]`](#health). |
| `resources` | table | no | See [`[resources]`](#resources). |
| `environment` | table of strings | no | See [`[environment]`](#environment-and-secrets). |
| `secrets` | table of strings | no | See [`[environment]`](#environment-and-secrets). |
| `runtime` | table | with databases | See [`[runtime]`](#runtime). |
| `databases` | array of tables | no | See [`[[databases]]`](#databases). |
| `schema_compatibility` | array of tables | no | See [`[[schema_compatibility]]`](#schema_compatibility). |
| `restore_invariants` | array of tables | no | See [`[[restore_invariants]]`](#restore_invariants). |

### `[health]`

| Key | Type | Default | Range |
| --- | --- | --- | --- |
| `path` | string | `/` | Absolute, clean HTTP path. No `%`, `\`, `?` or `#`. |
| `expected_status` | integer | `200` | 100 to 599. |
| `startup_deadline_seconds` | integer | `30` | 1 to 3600. |
| `timeout_seconds` | integer | `3` | 1 to 300, and not more than `startup_deadline_seconds`. |

### `[resources]`

| Key | Type | Rule |
| --- | --- | --- |
| `memory_mb` | integer | Positive 32-bit integer. Required when the table is present. |
| `pids_limit` | integer | Positive 32-bit integer. Required when the table is present. |

An omitted `[resources]` table resolves to the operator policy ceilings. Requests above a ceiling are refused.

### `[environment]` and `[secrets]`

`[environment]` maps variable names to literal string values. `[secrets]` maps variable names to Podman secret reference names, never values.

- Variable names match `[A-Za-z_][A-Za-z0-9_]*`. Names that start with `BRINE_` are reserved in both tables.
- A name cannot appear in both tables.
- Environment values may not contain `${`, NUL, or control characters other than tab, CR and LF. They must be valid UTF-8.
- A secret reference matches `[a-zA-Z0-9][a-zA-Z0-9_-]{0,252}`. The operator policy must list it under `allowed_secrets` for the app.

Set a secret value with `brine secret set APP NAME --target NAME`, which reads stdin.

### `[runtime]`

| Key | Type | Rule |
| --- | --- | --- |
| `uid` | integer | 1 to 65535. |
| `gid` | integer | 1 to 65535. |

Required when `[[databases]]` is present. It names the container user that owns the database files.

### `[[databases]]`

SQLite database declarations, at most 16. Each entry maps to storage the operator policy admits. See [Using a database](../using-a-database.md) for the workflow and the injected `BRINE_DB_<NAME>_*` variables.

| Key | Type | Required | Rule |
| --- | --- | --- | --- |
| `engine` | string | no | Only `sqlite` for an app with a container. |
| `name` | string | yes | `[a-z][a-z0-9-]{0,62}`. Unique, and unique after upper-casing with `-` mapped to `_`. |
| `persistent_root` | string | yes | Clean absolute path. Must lie under a policy `persistent_roots` entry. |
| `mount_path` | string | yes | Clean absolute container path. Mount paths of two databases cannot nest or match. |
| `filename` | string | yes | `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`. |
| `backup_destination` | string | yes | Names a policy `backup_destinations` entry. Same grammar as `name`. |
| `sync_interval` | string | no | A positive Go duration. Default `1m`. The policy `backup` table bounds it. |
| `schema` | string | no | Schema file path, relative to `brine.toml`. |
| `marker` | string | no | Schema marker, only with `schema`. Default is derived as `sha256:` plus 12 hex digits. |

A managed SpacetimeDB database uses `engine = "spacetimedb"` with a nested `[databases.spacetimedb]` table. Its keys are `image`, `version`, `database`, `module`, `runtime`, `resources`, `publisher`, `signing_private`, `signing_public` and `auth`, plus `recovery_password_ref` on the database entry. It must be the only database. The full contract is in [CONTRACTS](../CONTRACTS.md#command-surface) and the [SpacetimeDB runbook](../runbooks/spacetimedb.md).

### `[[schema_compatibility]]`

Optional, at most 16, at most one per database.

| Key | Type | Rule |
| --- | --- | --- |
| `database` | string | A declared database name. |
| `accepts` | array of strings | 1 to 128 unique, valid markers. Stored sorted. |
| `startup` | string | Only `preserve`. |

Without an entry, Brine accepts only the current schema file's marker and uses `startup = "preserve"`.

### `[[restore_invariants]]`

Optional typed checks that `brine restore test` runs on a restored copy. At most 64.

| `kind` | Required keys | Notes |
| --- | --- | --- |
| `row_count` | `database`, `table`, `count` | No `column`. |
| `non_null` | `database`, `table`, `column` | |
| `integer_range` | `database`, `table`, `column`, `minimum`, `maximum` | |

### Example

```toml
schema_version = 1
name = "hello"
image = "ghcr.io/team/hello@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
```

## Operator policy

The operator owns this file. On an enrolled server it is `/etc/ssh/brine/operator-policy.toml`, root-owned. Enrollment installs a default only when none exists. The default allows no registries, domains or secrets, and sets app ports 20000 to 20999, a 512 MiB memory ceiling, a 128 PID ceiling, a 1 GiB disk reserve and `caddy_port = 443`. `brine permissions` edits registries, domains and CDN bindings. Every other key needs a manual edit as root.

### Top-level keys

| Key | Type | Default | Rule |
| --- | --- | --- | --- |
| `schema_version` | integer | none, required | Must be `1`. |
| `version` | string | none, required | Operator revision token, `[A-Za-z0-9][A-Za-z0-9_.-]{0,127}`. |
| `resources.memory_mb` | integer | none, required | Positive 32-bit ceiling. |
| `resources.pids_limit` | integer | none, required | Positive 32-bit ceiling. |
| `app_ports.min`, `app_ports.max` | integer | `20000`, `20999` | At least 1024, ordered. |
| `minimum_free_disk_bytes` | integer | `1073741824` | Positive. Planning requires at least this much free disk. |
| `caddy_port` | integer | unset | Deployment currently requires `443`. |
| `persistent_roots` | array of strings | none | Clean absolute non-root paths that do not overlap. |
| `allow_live_restore` | boolean | `false` | Permits planned live restore. |
| `allow_archive_purge` | boolean | `false` | Permits reviewed archive purge. |

### Allow lists

| Key | Shape | Rule |
| --- | --- | --- |
| `allowed_registries` | array of `{host, repository_prefixes}` | `host` is an exact ASCII registry host with an optional port. A prefix matches that repository and its descendants. A host with no prefixes allows nothing. Aliases are not expanded: `docker.io/library/alpine` and `docker.io/alpine` are different. |
| `allowed_domains` | array of strings | Exact names or `*.suffix`. A wildcard matches one or more subdomain labels, never the apex. |
| `allowed_secrets` | table of app name to array of references | An absent app may use no secrets. References are case-sensitive. |

An empty or missing allow list denies everything.

### Backup and data tables

| Key | Shape | Notes |
| --- | --- | --- |
| `backup_destinations` | array of `{reference, endpoint, region, bucket, base_prefix, path_style, credential_ref}` | S3-compatible destinations. `reference` is what an app's `backup_destination` names. References must be unique. |
| `backup_retention` | array of `{destination, endpoint, bucket, base_prefix, verification_id, verified_at, freshness_seconds, no_object_expiration}` | Evidence that a destination keeps objects. Must match a declared destination. |
| `backup` | `{min_sync_interval, max_sync_interval, snapshot_interval, min_snapshot_interval, max_snapshot_interval}` | Go durations. Defaults `10s`, `1h`, `6h`, `1h`, `24h`. Min sync at least `10s`, max sync at most `1h`, snapshots between `1h` and `24h`. |
| `data_initialization` | `{max_backup_age_seconds, max_restore_test_age_seconds, recovery_window_seconds}` | Bounds for first initialization: at most 86400, 300 and 2592000. All positive when present. |
| `spacetimedb` | `{license_use, license_authority, capture_interval_seconds, verification_interval_seconds, max_remote_loss_seconds, max_maintenance_seconds}` | Required facts for managed SpacetimeDB. See [CONTRACTS](../CONTRACTS.md#command-surface). |
| `cdn_bindings` | array of typed bindings | Exact CDN attachments. See [CONTRACTS](../CONTRACTS.md#cdn-attachments-d14). |

### Example

```toml
schema_version = 1
version = "operator-1"
allowed_domains = ["*.example.com"]

[[allowed_registries]]
host = "ghcr.io"
repository_prefixes = ["team"]

[resources]
memory_mb = 512
pids_limit = 128

[allowed_secrets]
hello = ["hello-token"]
```

This is `examples/offline/policy.toml` with the unrelated lines removed.

## Client target directory

`brine enroll` writes the pinned target (SSH destination, host key pin, client identity) to a private directory on the client. Every connected command reads it.

| Platform | Default location |
| --- | --- |
| Linux | `$XDG_CONFIG_HOME/brine/targets`, or `~/.config/brine/targets` |
| macOS | `~/Library/Application Support/brine/targets` |

Pass `--config-dir DIR` to use another directory. Keep the same `--target` and `--config-dir` for the whole workflow. The default comes from Go's `os.UserConfigDir`.

## Machine mode and terminal output

| Flag or variable | Effect |
| --- | --- |
| `--json` | One JSON envelope on stdout. |
| `--jsonl` | One JSON envelope per line. Cannot be combined with `--json`. |
| `--no-input` | Fail with `input_required` instead of prompting. `--json` and `--jsonl` imply it. |
| `NO_COLOR` (non-empty) | Removes styling from human output. |
| `SSH_AUTH_SOCK` | When set, the SSH agent socket is passed through to OpenSSH for every command that opens an SSH connection. |

The envelope fields, error codes and exit codes are in [CONTRACTS](../CONTRACTS.md#machine-modes). CDN provider profiles read a token from an environment variable the profile names.
