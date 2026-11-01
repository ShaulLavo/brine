# Exact operator secret-reference permissions

Date: 2026-11-01. Local synthetic fixtures only. No enrolled host, registry,
operator policy, credential, secret value or deployment was changed.

## Scope

The existing root-only permission workflow accepts `allow_secret` and
`revoke_secret`, each containing only an exact typed app and logical reference.
`--secret-app APP` supplies the authority scope. `--app` remains audit metadata.
The existing pinned operator SSH channel, enrollment identity, root checks,
locks, journal, policy replacement and readback are unchanged.

Model the Domain kept this addition in the existing action union. Boundary
Discipline put validation in the policy editor and strict CLI/wire decoders.
Make Operations Idempotent kept the existing journal and byte-hash guards.
Prove It Works drove the failing-first tests and real CLI help regeneration.
No children or independent review were launched by this implementation lane.

## Verification

All checks used the installed shared PC runner. Availability reported no owner
reservation before and inside each job. Each job owned a temporary directory
under `/home/shaul/tmp`, on the alternate XFS SSD, with automatic teardown.
The existing two unrelated runner jobs were preserved.

Failing-first command:

```sh
go test ./internal/policy ./internal/enroll ./internal/cli -run TestSecretPermissions -count=1
```

It exited 1 before production edits. Exact grants were refused in policy tests,
the codec rejected the new typed variant, and CLI tests rejected unknown flags.

Passing checks:

```sh
scripts/check.sh test-normal
go test -race -p 1 -timeout 10m ./internal/policy ./internal/enroll ./internal/cli ./internal/secrets ./internal/dispatch -count=1
BRINE_BASE=a2886b35518b2c29d5ce59bb586269f0c618d259 scripts/check.sh lint
go run ./tools/checkgate rules --baseline tools/baselines/rules.json
go test ./internal/policy ./internal/enroll ./internal/cli -run 'TestSecretPermissions|TestPermissions|TestCDNPermission' -count=1
go test -race ./internal/policy -run TestSecretPermissions -count=1
git diff --check
```

`test-normal` exited 0 in 275.6 seconds. It includes Go tests, Linux and Darwin
arm64 vet, the CLI build, formatting, module checks and the WebTransport fixture.
The race/lint/rules job exited 0 in 97.2 seconds. Both lint passes reported zero
issues; rules reported 10 existing findings and zero new findings. After the
full run, the strengthened mixed-action fixtures passed the final targeted and
policy race commands above. The existing `TestSecretPolicyRefusal` also passed.

The command reference was regenerated with a built CLI and the existing
`scripts/docs-commands.sh`. `scripts/test-docs-commands.sh` passed. Neither script
changed. No feature version changed, and no UDP plan changed.

## Installation and operator follow-up

Build and install the same reviewed source on the client and enrolled host before
using the new flags. No policy schema or control-database migration is needed.
This lane did not release, install or deploy a binary. Existing admin authority
and target pin/identity are still required. The exact owner-approved signing
reference remains the parent's input, not a new value chosen by this lane.

```sh
brine permissions show --target "$TARGET" --admin "$ADMIN" --json --no-input
brine permissions plan --target "$TARGET" --admin "$ADMIN" --secret-app waffle-relay --allow-secret-ref "$SIGNING_SECRET_REF" --json --no-input
brine permissions apply --target "$TARGET" --admin "$ADMIN" --plan-id "$PLAN_ID" --idempotency-key "$PERMISSION_KEY" --json --no-input
brine permissions status --target "$TARGET" --admin "$ADMIN" --idempotency-key "$PERMISSION_KEY" --json --no-input
brine permissions plan --target "$TARGET" --admin "$ADMIN" --secret-app waffle-relay --revoke-secret-ref "$SIGNING_SECRET_REF" --json --no-input
```

Review the returned grant plan before applying it. An inverse revocation uses its
own returned plan ID and a new stable operation key. None of these proposed
commands was run against a host. Physical operator mutation remains unverified.

## Cleanup

All task-created temporary directories and the checkout's test-built `brine`
binary were removed after their consumers stopped. No task-created service,
container, mount, route, checkout, download or transferred binary remains.
The source changes and this small evidence record are the retained deliverables.
Shared Go caches and runner history remain owned by their existing consumers.
