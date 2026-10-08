# Phase 02: Enrolled host, Podman/Quadlet and Caddy

**Depends on:** validated spec/planner. **Goal:** support a single disposable fixture on one explicitly authorized Linux host. The authorized host is the owner's Raspberry Pi (Debian 13, arm64) within the limits in [D3](../DECISIONS.md). This is not authorization to modify the user's existing VPS.

### Tasks

- [ ] **P02-01** Read-only target inventory: distro, kernel, cgroup v2, systemd/user manager, Podman/Quadlet/Caddy versions, occupied host ports, existing services, routing, target identity and disk space. Emit the P01-07 snapshot type. Return unsupported/conflict errors instead of trying to “fix” unknown software; a non-Debian-13 host is `unsupported` (D2).
- [ ] **P02-02** Implement `brine enroll` over the operator's own SSH access (D1): pin the host key, create the runner user with lingering, install the `brine` binary, create Brine-owned directories and install the deploy key with `restrict,command="brine host serve"`. Implement `brine host serve` with a versioned JSON request/response and an operation allowlist, and make the client talk to it through the system OpenSSH binary. No passwordless root SSH; enrollment installs or removes nothing beyond what it lists.
- [ ] **P02-03** Build typed Podman and systemd adapters with argument arrays, timeouts, bounded stdout/stderr and fake implementations. Probe Quadlet generator, actual rootless user-service startup, and linger/reboot behavior on a pinned test OS.
- [ ] **P02-04** Render minimal, owned Quadlet units with digest-pinned image, a `127.0.0.1` port from the policy range (D6), fixed volume mapping and Podman `Secret=` references (D5). Stage outside active unit directories, atomic activation where feasible, validate generated units and avoid writing arbitrary templates supplied by apps.
- [ ] **P02-05** Build the Caddy adapter per [D4](../DECISIONS.md): one Brine-owned file per app under `/etc/caddy/brine.d/`, full-config `caddy validate`, atomic rename, polkit-scoped `systemctl reload caddy.service`, restore the previous file on failure, and hash-based drift detection. Test persistence through restart and that an app container cannot reach the admin API.
- [ ] **P02-06** Fixture integration: create a disposable public-free test service, route it via Caddy/test hostnames, run health checks, reboot, and prove it is still available without Brine running. Cleanup must target **only fixture-owned resources**.

### Exit gate

A documented, manually authorized fixture starts rootlessly, is routed through Caddy, survives reboot, and is not dependent on an active Brine process. Conflicts with any unrelated service abort without changes. Privilege gaps are explicitly recorded.

**Evidence:** T05, T06, T11, T17. Pin exact Podman/systemd/Caddy versions and capture redacted test output. Before real deployment, confirm the owner has explicitly authorized the *specific disposable host*.
