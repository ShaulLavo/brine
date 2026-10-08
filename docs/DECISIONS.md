# Brine architecture decisions

**Status: Approved** (owner, 2026-10-08). These decisions override older wording in the plans. If a decision proves wrong on real hardware, add a new numbered entry that supersedes it and update the affected tasks; do not edit history silently.

## D1. Brine runs on the host; the laptop is a client

The `brine` binary is installed on each enrolled host and runs as a dedicated, unprivileged **runner user** (rootless Podman, systemd user services, lingering enabled). All durable state lives there: the control SQLite database (plans, releases, operations, events, the host mutation lock) sits in the runner's state directory, separate from app databases.

The client (`brine` on a laptop or in an agent sandbox) holds only target configuration: SSH destination and pinned host key. It never holds plans, releases or operation state, so a human and an agent on different machines see the same plan IDs and operations.

- **Transport:** the client runs the system OpenSSH binary with typed arguments, so `~/.ssh/config`, the SSH agent, `known_hosts` and Tailscale addressing work unchanged. No shell strings are built.
- **Restricted dispatcher:** the runner's deploy key is installed with `restrict,command="brine host serve"` in `authorized_keys`. `brine host serve` reads one versioned JSON request from stdin, checks it against an allowlist of operations and the operator policy, and writes one JSON response (or a JSONL event stream). That key cannot open a shell, forward ports or run Podman, systemd or Caddy directly. This is the P06-01 authorization boundary.
- **Long-running operations:** `apply` records intent, then starts a transient user unit (`brine-op-<operation-id>`) that runs `brine host run-op <operation-id>`. The SSH session can drop without stopping it. Unit names and arguments come from Brine, never from the request.
- **Enrollment and upgrades** use the operator's own admin SSH access, not the deploy key. `brine enroll` creates the runner user, directories, the binary, lingering and the restricted key only after the operator confirms the read-only inventory. It installs or removes nothing else.
- **Offline plans** (`plan --offline`) run locally against a snapshot file and produce a plan file marked not applyable. They never enter the host's database.

## D2. Reference OS: Debian 13 (trixie)

Supported target: Debian 13 with systemd 257, cgroup v2 and the distribution's Podman 5.x and Caddy packages. The pinned versions are recorded once installed on the test host. Other distributions return `unsupported` from inventory rather than "probably works".

The VPS currently runs Ubuntu 24.04 and was cleared on 2026-10-08 down to SSH, Tailscale and mesh. It must run Debian 13 before Brine deploys anything there.

Hosts may be **amd64 or arm64**. Plans already bind image platform. Fixture images must be published for both architectures, and plans refuse an image digest whose platform doesn't match the target.

## D3. Test host: the owner's Raspberry Pi

The owner's Raspberry Pi (Debian 13, arm64, 4 GB RAM, SD-card storage) is the authorized disposable host for Phase 02–04 integration tests and reboot drills. It is reached over the owner's tailnet. Its hostname, addresses and inventory stay out of this repository.

Authorized on that host: installing Podman, Caddy and Litestream from Debian packages or pinned releases; creating the runner user; deploying fixture apps; rebooting it for drills. Not authorized: changing its tailnet, firewall or other services, or touching data that Brine didn't create. Cleanup removes only fixture-owned resources.

SD-card storage is slow and wears out. Keep fixture images small, and don't treat Pi timings as performance baselines.

## D4. Caddy: Brine-owned drop-in files, reloaded through systemd

Caddy is the Debian-packaged system service. The main Caddyfile gets one line at enrollment: `import /etc/caddy/brine.d/*.caddy`. Brine owns that directory (writable by the runner user) and writes one file per app. It never touches the main Caddyfile or other sites.

Change sequence: write the new file to a staging path, run `caddy validate` on the full config, atomically rename it into place, then `systemctl reload caddy.service`. Caddy keeps its previous config if the new one fails to load. If the reload fails, Brine restores the previous file. A polkit rule lets the runner user reload (not stop, restart or edit) `caddy.service` and nothing else. Drift detection compares owned files against hashes in the control database.

Config on disk means routes survive Caddy restarts and reboots without `--resume`. The admin API stays on Caddy's default local endpoint and is never handed to apps; rootless containers don't reach host loopback by default, and Phase 02 tests that an app container cannot connect to it.

**Coolify** was removed from the VPS at the owner's request on 2026-10-08, along with everything else except SSH, Tailscale and mesh. Brine itself never uninstalls other software: if inventory finds another proxy holding ports 80/443, it reports a conflict and refuses (T05).

Caddy isn't preinstalled anywhere. Enrollment installs the Debian `caddy` package only with the operator's confirmation, and then adds the `import` line itself.

## D5. Secrets: Podman secrets

App secrets are Podman secrets owned by the runner user, injected through Quadlet `Secret=<name>,type=env,target=<VAR>`. `brine.toml` names a secret reference only:

~~~toml
[secrets]
DATABASE_KEY = "hello-db-key"
~~~

Values are set on the host through the dispatcher (`brine secret set APP NAME`, value read from stdin, never from argv). Plans record secret names and Podman's secret IDs, never values. Rotation creates a new secret and a new plan.

Limits: Podman's default `file` driver stores values unencrypted with owner-only permissions in the runner's storage, so anyone with runner or root access can read them. That matches the v1 threat model (single operator, no untrusted co-tenants). An encrypted operator-side store such as sops/age can be added later as an input source without changing the app contract.

Other credentials follow the same rule. Registry pulls use the runner's Podman auth file (mode 0600). Litestream's R2 credentials live in a runner-owned 0600 file loaded by the Litestream user unit, scoped per destination. None of these are visible to app containers.

## D6. Host ports for apps

Containers publish only on `127.0.0.1`. Brine allocates each app a host port from a range set in target policy (default `20000-20999`), records it in the control database and keeps it stable across releases so a rollback doesn't need a route change. Inventory reports ports already in use, and allocation skips them.
