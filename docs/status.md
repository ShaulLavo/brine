# Status and limits

Brine is pre-1.0 and experimental. It has one maintainer, no release and no tag. This page says what works, what does not exist yet, and what has been tested on real hardware.

## Stability

- The CLI version is a development value, `0.1.72-dev`.
- The JSON envelope has `schema_version` 1 and is covered by tests. It is not yet a stable public API, so pin a commit if you build tooling on it.
- Only `main` is maintained. There are no release binaries, so you build from source. See the [README quick start](../README.md#quick-start).

## What exists

| Area | State |
| --- | --- |
| Validation and planning, offline and connected | Implemented. |
| Enrollment, restricted dispatcher, operator permissions | Implemented. |
| Detached deploy, status, logs, diagnose, reconcile, resolve | Implemented. |
| Config and secret changes, start, stop, restart, rollback | Implemented. |
| Stateless app removal | Implemented. Removal of an app with persistent data is refused. |
| SQLite databases, Litestream backups, isolated restore tests, reviewed first initialization | Implemented. Some acceptance drills are still open. |
| Managed SpacetimeDB commands | Implemented. Module-update, live-restore, archive and fault drills are still open. |
| CDN attachments (Cloudflare first) | Implemented behind its own commands. Physical provider acceptance is still open. |
| Migrations for an existing database schema | Not implemented. |
| Full Charm terminal UI | Not built. `brine tui` shows a welcome screen. |
| Static drops, git previews | Not built. |
| UDP and WebTransport publishing | Not built. |

## Platform support

| Part | Supported | Notes |
| --- | --- | --- |
| Server | Debian 13 on amd64 or arm64 | Systemd 257, cgroup v2, Podman 5.4, `passt`, `uidmap` and Caddy 2.6 from the distribution. Other distributions return `unsupported` from inventory. |
| Client | macOS arm64, Linux | CI runs the tests on Linux and vets the macOS arm64 build. Windows is untested. |
| Container images | Linux amd64 and arm64 | Pinned by digest, from a registry that allows anonymous pulls. Private registries that need credentials are not wired. |
| Build | Go 1.26 or newer | `mise.toml` pins Go 1.27.3 for CI. |

## Limits by design

- One enrolled host, one trusted operator, and one web container per app.
- Prebuilt images only. Brine does not build images on the server.
- One local mutation at a time per host. Read-only status and logs can run at the same time.
- A deployment accepts brief downtime. Zero downtime is not claimed, and Quadlet and Caddy do not provide it by themselves.
- Not a multi-tenant host. Brine deploys code written by its owner and the owner's agents. See [SECURITY.md](../SECURITY.md).
- No web dashboard, no multi-server orchestration, no Kubernetes, and no creating or deleting cloud machines.
- Application rollback never rewinds a database. Database restore is a separate, explicit operation.
- Backups are asynchronous. A restore check proves recoverability. It does not prove zero data loss.

## What has been tested on real hardware

Deploy, rollback, killed-client, interrupted-operation recovery, reboot and removal drills have run on one Raspberry Pi (arm64, Debian 13). The same host ran SQLite initialization, a persistent-app release and an isolated restore from S3-compatible storage. Managed SpacetimeDB first publication and online capture with replay to R2 ran on both arm64 and amd64.

Those drills do not amount to a production-readiness claim. Local unit and integration tests use fakes, and CI does not touch a server.

## License

[MIT](../LICENSE).
