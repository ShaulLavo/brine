# Phase 02: Enrolled host, Podman/Quadlet and Caddy

**Depends on:** validated spec/planner. **Goal:** support a single disposable fixture on one explicitly authorized Linux host. This is not authorization to modify the user's existing VPS.

### Tasks

- [ ] **P02-01** Read-only target inventory: distro, kernel, cgroup v2, systemd/user manager, Podman/Quadlet/Caddy versions, occupied host ports, existing services, routing, target identity and disk space. Return unsupported/conflict errors instead of trying to “fix” unknown software.
- [ ] **P02-02** Define operator-driven enrollment with known host key, Tailscale/SSH transport as applicable, a distinct runner identity and restricted configuration directories. No passwordless root SSH and no automatic install/uninstall.
- [ ] **P02-03** Build typed Podman and systemd adapters with argument arrays, timeouts, bounded stdout/stderr and fake implementations. Probe Quadlet generator, actual rootless user-service startup, and linger/reboot behavior on a pinned test OS.
- [ ] **P02-04** Render minimal, owned Quadlet units with digest-pinned image and fixed port/volume mapping. Stage outside active unit directories, atomic activation where feasible, validate generated units and avoid writing arbitrary templates supplied by apps.
- [ ] **P02-05** Build Caddy adapter using a dedicated owned config subtree or explicitly adopted instance and a protected admin socket. Validate before replace, detect drift, keep unrelated routes intact, test persistence through restart. Ensure no app can connect to its admin API.
- [ ] **P02-06** Fixture integration: create a disposable public-free test service, route it via Caddy/test hostnames, run health checks, reboot, and prove it is still available without Brine running. Cleanup must target **only fixture-owned resources**.

### Exit gate

A documented, manually authorized fixture starts rootlessly, is routed through Caddy, survives reboot, and is not dependent on an active Brine process. Conflicts with any unrelated service abort without changes. Privilege gaps are explicitly recorded.

**Evidence:** T05, T06, T11, T17. Pin exact Podman/systemd/Caddy versions and capture redacted test output. Before real deployment, confirm the owner has explicitly authorized the *specific disposable host*.
