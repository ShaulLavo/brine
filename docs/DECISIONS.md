# Brine architecture decisions

**Status: Approved** (owner, 2026-10-08). These decisions override older wording in the plans. If a decision proves wrong on real hardware, add a new numbered entry that supersedes it and update the affected tasks; do not edit history silently. Approved means designed, not implemented.

## D1. Brine runs on the host; the laptop is a client

The `brine` binary is installed on each enrolled host and runs as a dedicated, unprivileged **runner user** (rootless Podman, systemd user services, lingering enabled). All durable state lives there: the control SQLite database (plans, releases, operations, events, the host mutation lock) sits in the runner's state directory, separate from app databases.

The client (`brine` on a laptop or in an agent sandbox) holds only target configuration: SSH destination and pinned host key. It never holds plans, releases or operation state, so a human and an agent on different machines see the same plan IDs and operations.

- **Transport:** the client runs the system OpenSSH binary with typed arguments, so `~/.ssh/config`, the SSH agent, `known_hosts` and Tailscale addressing work unchanged. No shell strings are built.
- **Restricted dispatcher:** the runner's deploy key is installed with `restrict,command="/usr/local/bin/brine host serve"` (an absolute, operator-owned path) in `authorized_keys`. `brine host serve` ignores `SSH_ORIGINAL_COMMAND` for dispatch. It reads one bounded, versioned JSON request from stdin, checks it against an allowlist of operations and the operator policy, and writes one JSON response (or a JSONL event stream). That key cannot open a shell, forward ports or run Podman, systemd or Caddy directly. The binary, the policy file and `authorized_keys` are owned by root or the operator, and D7 says how they stay that way. This is the P06-01 authorization boundary.
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

## D7. The runner can't replace its own SSH restriction

Added 2026-10-08 after the [Pi runtime spike](spikes/pi-runtime.md). It replaces D1's ownership sentence for the restricted key.

Making `authorized_keys` root-owned isn't enough. If the runner owns any parent directory, it can rename `.ssh` and put back an unrestricted key. The spike demonstrated this.

Enrollment therefore makes the runner's home operator-owned (`root:<runner>`, mode `0755`) and its `.ssh` root-owned (`0755`) and empty after verification. The deploy key lives outside the home at `/etc/ssh/brine/authorized_keys/<runner>` (root-owned `0644`), under root-owned `0755` directories. Writable state remains in runner-owned `.config`, `.local`, `.cache` and Brine state subdirectories.

Ownership alone cannot rule out alternate authorization sources in remote-only SSH Match branches. Enrollment now changes SSH configuration through one root-owned `0644`, journaled drop-in, `/etc/ssh/sshd_config.d/00-brine-<runner>.conf`. Its runner-only Match block forces `/usr/local/bin/brine host serve` for every login (including certificate authorization), forces the external key path, disables authorized-key commands and principals files, and disables PTY, TCP/agent/X11/stream-local forwarding, tunnels and gateway ports. This replaces the earlier claim that enrollment avoids changing SSH configuration; other users retain their existing settings. The main configuration must begin with Debian's unconditional `Include /etc/ssh/sshd_config.d/*.conf`, and no earlier-sorting drop-in may precede Brine's policy. Arbitrary later Include and Match syntax is evaluated by sshd, not a hand-written authorization-source parser.

`PermitUserEnvironment` cannot appear inside Match on the supported OpenSSH server. Enrollment does not change it: its effective global value must already be exactly `no`, never `yes` or a variable pattern, for every tested IPv4/IPv6 loopback and remote connection specification. The same specifications verify every forced runner setting. The staged full configuration must pass `sshd -t` before promotion and SSH reload. Known promotion/reload failure restores journal-verified original state and reloads it. An unknown reload outcome keeps a durable marker and refuses automatic apply retries; explicit operator undo restores and validates the on-disk original before reloading. Undo removes only verified Brine-owned policy, key files and directories and reloads SSH.

SSH environment settings are another pre-dispatch boundary: a client-sent `LD_PRELOAD` can execute loader code in `/bin/sh` before the dispatcher clears its environment. Enrollment requires effective `AcceptEnv` to contain only `LANG`, `LC_*`, and the exact display-only names `COLORTERM` and `NO_COLOR` (a subset or empty list is safe; no display-name wildcards are allowed), and `SetEnv` to set only concrete locale variables, never loader/shell/path/runtime variables. Because AcceptEnv is additive, the early runner drop-in cannot override unsafe later entries. In addition to the four effective probes, a bounded source audit checks environment directives in every included Match branch, including unsampled addresses and other users. It recognizes equals and double-quoted Include forms but accepts only literal absolute paths and Debian's exact `/etc/ssh/sshd_config.d/*.conf` pattern. The canonical pattern excludes dotfiles and sorts names bytewise, matching glob(3) under the controlled `LC_ALL=C` parser environment. Character classes (including POSIX negation), `?`, braces, tilde, relative paths, other wildcard patterns, ambiguous escapes and Includes inside included files are refused. Before mutation and during staged validation, bounded `sshd -ddd -T` debug output must show exactly the audited unique source load order. Missing, truncated, extra or reordered source traces refuse enrollment. Enrollment never changes an operator's existing environment policy to make it pass. Unsupported or unsafe sources refuse enrollment with a clear environment-specific reason.

A forced command still runs through the account's login shell (`$SHELL -c`), and Debian's bash reads `~/.bashrc` even for that. A runner-owned `.bashrc` would run attacker code before the dispatcher. So enrollment:

- creates the runner with an empty skeleton, so no shell startup files (`.bashrc`, `.profile`, `.bash_logout`) exist;
- sets its login shell to `/bin/sh` (dash), which reads no startup files for a non-login `-c` command;
- keeps the home's top level limited to `.ssh` and the runner-owned subdirectories above, with no symlinks into writable paths;
- checks the effective sshd and PAM settings and refuses to enroll if `PermitUserEnvironment` or per-user PAM environment files are enabled.

Enrollment and P06-01 tests run as the runner and must fail to: replace the key file or any parent directory; create a top-level startup file; or run anything but the dispatcher through the real restricted key, including when a startup file has been planted.

Enrollment must also account for packages that start services on install. On the spike host, installing Caddy enabled and started it at once with its default site. Installing netavark enabled its DHCP proxy units, activated the DHCP proxy socket, and enabled a firewalld-reload unit. Enrollment lists these effects before asking for confirmation. It prevents Caddy from starting on install, for example by masking it first, so the default site is never exposed before Brine's config is in place.

## D8. Agents manage the whole lifecycle, through Brine operations only

Added 2026-10-09 by the owner. Brine exists so an agent (ChatGPT, Claude, Codex or a script) can run everything on a host, not only deploy. "Restricted" means the agent acts only through Brine's typed, recorded operations, never a shell. It doesn't mean the agent may do little.

The dispatcher's allowlist (D1) grows to cover:

- **App lifecycle:** deploy, update config (environment, resources, domains, health), set and rotate secrets, restart, stop and start, roll back, remove.
- **Diagnostics,** all read-only: status, logs, health, operation history and diffs, inventory, Caddy routing state, disk and memory use, and a `diagnose` report that gathers these for one app or the host.
- **Backups:** status and test restores; live restores as a planned operation, gated by policy like purge (P04-09).
- **Host operations:** updating the packages Brine manages (Podman, passt, Caddy, Litestream), restarting Caddy, cleaning Brine-owned leftovers such as old images, generations and releases, and rebooting. These come last (Phase 06), with the guarantees below.

Rules for every mutating operation:

- It is planned, then applied. It is journaled with its operation ID and requester, and is idempotent or reconciles after an unknown outcome.
- An app rollback still never rewinds data.

**Removing an app** stops it, removes its unit and route, quiesces its writers and replicator, and moves its data directory into an archive with an immutable archive ID. That ID binds the data path and the backup destinations recorded at removal time. Retention runs 30 days from the time removal committed. Brine keeps a restorable backup set for that whole period, and refuses removal if an R2 lifecycle rule or a still-running replicator would break that. Expiry and `brine data purge ARCHIVE_ID` act only on an archive ID, never an app name, so removing an app, recreating it and then purging the old archive can't touch the new app's data or replica. Both refuse any path or destination that a live app uses. Purge deletes early and is allowed for the agent key only when the operator policy sets `allow_agent_purge = true`; the default is false. Purge is never part of app removal, rollback or image cleanup. Stateless apps can be removed in Phase 03; archiving persistent data needs Phase 04's data layout.

**Live restore** replaces an app's database from a chosen backup. It is a planned operation (P04-09) with its own policy flag `allow_agent_live_restore`, default false. It needs a fresh restore point of the current data first, quiesced writers and replicator, an integrity check of the restored copy before swap-in, and recovery after interruption. Until P04-09 ships, live restore stays the operator runbook.

**Host operations** run through a small root-owned helper. The runner has no root shell or sudo. Typed arguments alone don't make the helper safe: it would be a confused deputy if it trusted runner-writable state. So:

- The helper re-checks authorization itself, against the root-owned operator policy and root-owned enrollment records. It never relies on the runner's database, an operation name or a caller's claim of approval.
- It accepts only fixed verbs, whose targets come from those protected records. It takes no executable, argv, environment, unit name or path from the caller.
- It opens runner-writable files without following symlinks and validates what it reads.
- It runs under the same host mutation lock and journal protocol as apply.
- The runner can't modify the helper, the policy, the enrollment records or any of their parent directories.
- Phase 06 picks the mechanism, such as a polkit-started root unit per verb, and T18 tests direct helper invocation, forged operation records and path or symlink substitution.

Host maintenance is planned like everything else:

- A package update binds the exact versions and the package-manager transaction. It refuses removals or new packages outside Brine's set, and declares which services restart and which apps and sites see downtime.
- Updates and reboots take the host mutation lock, so they never overlap a deploy.
- A reboot records its intent durably before it starts. After boot, Brine reconciles: it checks app health and never reissues a reboot that already happened.
- D4 still holds: the runner's polkit rule stays reload-only. A Caddy restart is a separate helper verb, gated by policy. It validates the live config first and is declared as affecting every site on the host, Brine's or not.

**Still out of reach for agents:** any shell or raw Podman, systemd or Caddy access; uninstalling or changing software Brine didn't install; other users' files; firewall, Tailscale and DNS changes; and creating or deleting cloud machines. The operator policy can narrow the allowlist further by operation class, but can't widen it past this list.

## D9. Drops and git previews: Vercel-fast deploys on our own server

Added 2026-10-09 by the owner. Deploying a website should take one command or one `git push`. Two new concepts sit beside full apps:

- A **drop** is a temporary website an agent or person publishes to show something: static files, or a small server. It gets a short, readable link such as `https://7k3d.shaulavo.dev`.
- A **preview** is a deployment of a git branch or pull request. Pushing to main deploys production as before.

**Owner-authored code only.** Drops and previews run the owner's own agents, scripts, and repositories. Brine is not a third-party hosting service. D4/D5's trusted-owner premise remains, with stronger drop isolation as defense in depth. Rootless containers alone do not establish that boundary.

**Private first.** Every drop starts private with expiring, revocable view grants. Publish is one-way; public drops hide the pill and are managed only through CLI. The pill, ported from mesh commit `78eeeaa`, is UI only. Browser publish, delete, and share open the reserved trusted origin `brine.shaulavo.dev`, where the server checks owner authorization, Origin/CSRF protection, and fresh action/drop-incarnation-bound confirmation. Drop content never receives management credentials. Dedicated redemption endpoints serve no drop content, atomically consume expiring random tokens, set Secure/HttpOnly/host-only cookies, and redirect before content with no-store and no-referrer. Capabilities stay out of logs, upstreams, and third-party resources.

**Shared-domain limits.** Drops use `shaulavo.dev` subdomains. The apex and reserved services are excluded. The gateway rewrites or strips upstream Domain cookies to host-only, refuses and strips upstream `__Host-brine*` request/response cookies, and reserves `__Host-` credentials for itself. Drop JS can still plant parent-domain cookies with `document.cookie`; that residual risk is accepted for owner-authored code. Chromium/WebKit tests cover JS-set and server-set cross-drop cookies without claiming full sibling isolation. Production apps holding sessions should not live on this domain's subdomains while drops share it. Public drops also risk the domain's reputation through phishing, spam, and blocklists. Third-party content would first require a dedicated drop domain registered on the Public Suffix List and a new security review.

**One managed gateway.** Caddy handles TLS; a Brine gateway serves files or proxies server drops. Its typed infrastructure site belongs to D4's same generation, single import, whole-config validation, drift detection, and rollback. Full-app routes cannot overlap the complete drop namespace or reserved names. No app supplies Caddy text. P07-01 decides DNS-01 or on-demand TLS and records exact operator-owned enrollment changes, explicitly superseding affected D2 package and D4 global-config assumptions. DNS-01 needs a pinned module/binary, upgrade contract, and protected DNS credential; on-demand TLS needs protected global configuration and `ask` admission. Neither grants agent DNS changes or a second import.

**Bounded server drops.** Use hardened rootless containers with no added capabilities, default seccomp, read-only root filesystems plus bounded tmpfs, resource/storage quotas, and no sockets or host mounts. Egress cannot reach host, private/tailnet/link-local/metadata addresses, control endpoints, production databases, or other drops, including through DNS rebinding or redirects. A spike selects and tests the exact mechanism; if enforcement cannot be proved, server drops get no network. `--run` is argv, never a host shell. Sources must already be runnable, with dependencies vendored or built in CI; no host installs or compiles. Idle servers stop and restart through recorded operations.

**Disposable, never reassigned.** Names are never reused. Tokens and cleanup bind to an immutable incarnation. TTL is 24 hours from creation or the latest authorized successful foreground content navigation; bots, denied requests, prefetch, HEAD, pill/status traffic, and stream frames do not renew it. `--keep` pins until explicitly unpinned. Drops and previews have no production mounts, secrets, or backup destinations. Unlike D8 app removal, their separately scoped delete/expiry permanently disposes of managed data: revoke access, drain streams, stop writers, then clean up through planned, journaled, reconcilable operations. This scope cannot purge apps or archives. Aggregate host byte/inode quotas reserve production/control capacity. Public hosting waits for abuse limits, emergency suspension, a disable-new-public switch, and auditable takedown.

**CI previews, separate authority.** Builds happen in CI. Preview and production credentials are separate; the dispatcher checks verb, environment, repository, and resource scopes for plan/apply. Preview keys never request production plans or touch production secrets. No fork-PR previews in v1; never use `pull_request_target` with fork checkout. Hostnames derive from stable repository ID, PR number, and a collision-resistant incarnation suffix; branch text is display only. Close-event cleanup targets the exact incarnation. The GitHub App is deferred outside the phase gate.

All mutations, including static drops, depend on P03-01 to P03-05 and P03-07. P07-12 safety gates precede public drops. Phase 07 host runs need explicit owner authorization naming the target and actions; D3's Pi grant covers Phases 02-04 only. The [Phase 07 plan](plans/07-drops-and-previews.md) defines dependencies, timing fixtures, and negative security tests.
