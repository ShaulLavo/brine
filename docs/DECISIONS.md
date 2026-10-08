# Brine architecture decisions

**Status: Approved** (owner, 2026-10-08). These decisions override older wording in the plans. If a decision proves wrong on real hardware, add a new numbered entry that supersedes it and update the affected tasks; do not edit history silently. Approved means designed, not implemented.

## D1. Brine runs on the host; the laptop is a client

The `brine` binary is installed on each enrolled host and runs as a dedicated, unprivileged **runner user** (rootless Podman, systemd user services, lingering enabled). All durable state lives there: the control SQLite database (plans, releases, operations, events, the host mutation lock) sits in the runner's state directory, separate from app databases.

The client (`brine` on a laptop or in an agent sandbox) holds only target configuration: SSH destination and pinned host key. It never holds plans, releases or operation state, so a human and an agent on different machines see the same plan IDs and operations.

- **Transport:** the client runs the system OpenSSH binary with typed arguments, so `~/.ssh/config`, the SSH agent, `known_hosts` and Tailscale addressing work unchanged. No shell strings are built.
- **Restricted dispatcher:** the runner's deploy key is installed with `restrict,command="/usr/local/bin/brine host serve"` (an absolute, operator-owned path) in `authorized_keys`. `brine host serve` ignores `SSH_ORIGINAL_COMMAND` for dispatch. It reads one bounded, versioned JSON request from stdin, checks it against an allowlist of operations and the operator policy, and writes one JSON response (or a JSONL event stream). That key cannot open a shell, forward ports or run Podman, systemd or Caddy directly. The binary, the policy file and `authorized_keys` are owned by root or the operator and are not writable by the runner. This is the P06-01 authorization boundary.
- **Long-running operations:** `apply` records intent, then starts a transient user unit (`brine-op-<operation-id>`) that runs `brine host run-op <operation-id>`. The SSH session can drop without stopping it. Unit names and arguments come from Brine, never from the request. Transient units don't survive a reboot and finished ones get garbage-collected, so the control database is authoritative: on startup, Brine reconciles any operation the database shows as unfinished.
- **App services** are Quadlet units with `[Install] WantedBy=default.target`, so the runner's user manager starts them at boot through lingering. Generated Quadlet services can't be `systemctl enable`d like ordinary units.
- **Enrollment and upgrades** use the operator's own admin SSH access, not the deploy key. `brine enroll` runs a read-only inventory, shows the complete list of changes, and applies only that list after the operator confirms. The list is fixed: the runner user and lingering, Brine's directories, the binary, the restricted deploy key, the Caddy polkit rule and import line (D4), and, if missing, the Debian `podman` and `caddy` packages. It never removes software.
- **Offline plans** (`plan --offline`) run locally against a snapshot file and produce a plan file marked not applyable. They never enter the host's database.

## D2. Reference OS: Debian 13 (trixie)

Supported target: Debian 13 with systemd 257, cgroup v2, and the distribution's Podman 5.4, `passt` and Caddy 2.6 packages. Test evidence records the exact installed package revisions. Inventory checks `passt` explicitly, since Podman only recommends it. Other distributions return `unsupported` from inventory rather than "probably works".

Hosts may be **amd64 or arm64**. Plans already bind image platform. Fixture images must be published for both architectures, and plans refuse an image digest whose platform doesn't match the target.

## D3. Test host: the owner's Raspberry Pi

A Raspberry Pi owned by the project owner, running Debian 13 on arm64, is the authorized disposable host for Phase 02–04 integration tests and reboot drills. Its name, address and inventory stay out of this repository; workers get them from the coordinator.

Authorized on that host: installing Podman, Caddy and Litestream from Debian packages or pinned releases; creating the runner user; deploying fixture apps; rebooting it for drills. Not authorized: changing its tailnet, firewall or other services, or touching data that Brine didn't create. Cleanup removes only fixture-owned resources.

Small single-board test hosts are memory- and IO-constrained. Keep fixture images small, and don't treat their timings as performance baselines.

## D4. Caddy: a Brine-owned config set, validated whole, reloaded through systemd

Caddy is the Debian-packaged system service. Enrollment adds one line to the main Caddyfile: `import /etc/caddy/brine/current/*.caddy`. `/etc/caddy/brine/current` is a symlink to a generation directory (`/etc/caddy/brine/gen-<n>/`) holding one Brine-rendered file per app. Brine never edits the main Caddyfile after enrollment or touches other sites.

Change sequence, under the host mutation lock:

1. Render the complete next app set into a new `gen-<n+1>/` (unchanged apps copied, the changed app replaced or removed).
2. Build a candidate root config: a temporary copy of the main Caddyfile with Brine's import line pointed at `gen-<n+1>/`. Run `caddy validate` on exactly that candidate. This catches invalid replacements and duplicate site addresses before anything is live.
3. Atomically repoint `current` to `gen-<n+1>/`, then `systemctl reload caddy.service`.
4. If the reload fails, repoint `current` to `gen-<n>/` and reload again. A reload that times out has an unknown outcome: reconcile by checking what Caddy is actually serving before restoring anything.

Keep the previous generation for rollback and prune older ones. A polkit rule lets the runner user call `org.freedesktop.systemd1.manage-units` only with `unit == "caddy.service"` and `verb == "reload"`. It grants no other verb, including `reload-or-restart`. Drift detection compares the live generation's files against hashes in the control database.

App files are rendered by Brine from typed fields only: site address, `reverse_proxy` to the app's localhost port, and fixed headers. They never contain global options, `admin` settings or app-supplied Caddyfile text. A failed Caddy 2.6 load can still restart the admin listener, which is another reason global settings stay fixed.

**Trust limit:** the runner can write imported Caddy config and reach the local admin endpoint, so a compromised runner can reroute any domain on the host. For v1 (one operator, trusted apps) the forced dispatcher and operator policy are the boundary, not file ownership or the polkit rule. If that becomes unacceptable, a separately privileged helper that validates and installs config replaces direct runner writes.

Config on disk means routes survive Caddy restarts and reboots without `--resume`. The admin API stays on Caddy's default loopback endpoint. With Podman 5.4's default pasta networking, app containers can't reach host loopback. Phase 02 tests that denial under the exact network mode Brine generates. Brine never generates host networking, loopback-enabling pasta or slirp options, or host socket mounts, and the inventory refuses an admin endpoint bound off loopback.

Brine never uninstalls other software. If inventory finds another proxy holding ports 80/443, it reports a conflict and refuses (T05).

## D5. Secrets: Podman secrets

App secrets are Podman secrets owned by the runner user, injected through Quadlet `Secret=<name>,type=env,target=<VAR>`. `brine.toml` names a secret reference only:

~~~toml
[secrets]
DATABASE_KEY = "hello-db-key"
~~~

Values are set on the host through the dispatcher (`brine secret set APP NAME`, value read from stdin, never from argv). Podman resolves environment secrets by name, so Brine stores each value under an immutable, versioned Podman name (`brine-<app>-<ref>-v<n>`) and never reuses a name. Plans record those versioned names, never values. Rotation creates a new version and a new plan. Old versions stay until no retained release references them, so rollback keeps working. The dispatcher only lets an app's operations see that app's secrets.

Limits: Podman's default `file` driver stores values unencrypted with owner-only permissions in the runner's storage, so anyone with runner or root access can read them. That matches the v1 threat model (single operator, no untrusted co-tenants). An encrypted operator-side store such as sops/age can be added later as an input source without changing the app contract.

Other credentials follow the same rule. Registry pulls use the runner's Podman auth file (mode 0600). Litestream's R2 credentials live in a runner-owned 0600 file loaded by the Litestream user unit, scoped per destination. Brine never mounts credential directories, the Podman socket or other runtime sockets into app containers.

## D6. Host ports for apps

Containers publish only on `127.0.0.1`. Brine allocates each app a host port from a range set in target policy (default `20000-20999`), records it in the control database and keeps it stable across releases so a rollback doesn't need a route change. Inventory reports ports already in use, and allocation skips them.
