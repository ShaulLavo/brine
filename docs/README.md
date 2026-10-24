# Brine docs

Start with [Getting started](getting-started.md). It runs on your laptop and needs no server.

## Tutorial

- [Getting started](getting-started.md): validate an app, preview three plans, read them as JSON.

## Guides

- [Deploy an app to a server](guides/deploy-an-app.md): enroll, allow, plan, apply, roll back, remove.
- [Using a database](using-a-database.md): declare a SQLite database and deploy it with one command.
- [Operate a managed SpacetimeDB app](runbooks/spacetimedb.md): the required order of the SpacetimeDB commands.
- [Troubleshooting](troubleshooting.md): stuck, refused and uncertain operations.

## Concepts

- [Architecture](ARCHITECTURE.md): the layers, the restricted dispatcher and the guardrails.
- [Reviewed first-database initialization](data-initialization.md): what the initialization guarantees and when it stops.
- [Status and limits](status.md): stability, platform support and what has been tested.

## Reference

- [Commands](reference/commands.md): every command and flag, generated from `--help`.
- [Configuration](reference/configuration.md): `brine.toml`, operator policy, client directory.
- [Contracts](CONTRACTS.md): JSON envelope, error codes and exit codes, and the behavior contract of each command family.
- [Decisions](DECISIONS.md): D1 to D14, the accepted design decisions and their reasons.

## Operator notes for managed SpacetimeDB

These pages back the runbook above. They are dense and written for someone operating the feature.

- [Native backup commands](native-backup-commands.md)
- [Native material delivery](native-material-commands.md)
- [Reviewed native module publication](native-module-commands.md)
- [Recover an interrupted native restore](native-restore-recovery.md)
- [Managed engine startup](managed-engine-startup.md)
- [Managed ingress logging prerequisite](managed-ingress-logging.md)

## Operator notes for SQLite backups

- [Protected Litestream operator drill](litestream-operator-drill.md): install Litestream on an already enrolled host and check the credential packet format.

## Contributing

[CONTRIBUTING.md](../CONTRIBUTING.md) covers setup, the check script and the rules that have an executable check. [AGENTS.md](../AGENTS.md) is for coding agents.
