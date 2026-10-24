# Security policy

Brine changes real servers. It installs packages and an SSH restriction during enrollment, runs containers, edits Caddy configuration, and holds app secrets and backup credentials. Reports about those boundaries are welcome.

## Report a vulnerability

Use GitHub's private vulnerability reporting: open the repository's **Security** tab and choose **Report a vulnerability**, or go to <https://github.com/ShaulLavo/brine/security/advisories/new>.

If that page is not available, open a public issue titled "Security contact request". Put no details in it. The maintainer will reply with a private channel.

Include the commit or version (`brine version`), what you did, what happened, and what you expected. Redact real hostnames, keys, tokens and backup credentials. A report with a reproduction against fixture data is easiest to act on.

There is no bug bounty and no response-time commitment. Brine is a personal project.

## Supported versions

Brine is pre-1.0 and has no releases or tags. Only the `main` branch receives fixes. The version string is a development value (`brine version` prints `0.1.72-dev` at the time of writing).

## Trust model

Brine is a personal deployment service. It deploys code written by its owner and the owner's agents, using the owner's scoped credentials. It is not a multi-tenant host, and strangers never upload code to it. Visitors to a deployed site are untrusted. Hostile uploaders are out of scope. See [status](docs/status.md) for the full list of limits.

## What counts as a vulnerability

These boundaries are intended to hold, so a break is a vulnerability:

- The restricted deploy key can run only `brine host serve`. It gets no shell, no port forwarding and no PTY ([D1](docs/DECISIONS.md#d1-brine-runs-on-the-host-the-laptop-is-a-client), [D7](docs/DECISIONS.md#d7-the-runner-cant-replace-its-own-ssh-restriction)).
- An agent that holds only the deploy key cannot widen operator policy, read secret values, reach the Podman socket or run commands outside the dispatcher's allowlist ([D8](docs/DECISIONS.md#d8-agents-manage-the-whole-lifecycle-through-brine-operations-only)).
- Secret values never appear in plans, logs, operation records or command-line arguments ([D5](docs/DECISIONS.md#d5-secrets-podman-secrets)).
- A plan that the policy refuses cannot be applied, and a stale plan cannot be applied.
- App rollback never rewinds a database. Removing an app that has persistent data is refused with `persistent_archive_required`, and nothing is deleted.

## Known limits, not vulnerabilities

- A compromised runner account can rewrite imported Caddy configuration and reroute any domain on the host ([D4](docs/DECISIONS.md#d4-caddy-a-brine-owned-config-set-validated-whole-reloaded-through-systemd)).
- Podman's default `file` secret driver stores values unencrypted with owner-only permissions. Anyone with runner or root access on the host can read them ([D5](docs/DECISIONS.md#d5-secrets-podman-secrets)).
- Anyone who holds the deploy key can plan and apply anything the operator policy allows. Policy is the boundary, not the key.
- The test evidence is limited to one Raspberry Pi. Brine does not claim production readiness.
