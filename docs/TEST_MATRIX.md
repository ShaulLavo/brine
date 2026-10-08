# Brine acceptance and failure test matrix

**This is a test plan, not a report of passing deployments.** No real VPS is authorized by this file. Unit tests use synthetic fixtures and fake adapters; destructive integration tests require an explicitly named disposable target and clean test credentials.

| ID | Scenario | Required evidence | Phase |
| --- | --- | --- | --- |
| T01 | Human and JSON version/doctor output | Valid schema, no ANSI or prompts in machine output | 00 |
| T02 | Invalid config, unknown field, unsafe path/domain/image | Validation refusal; no host or control-plane effects | 01 |
| T03 | Repeated same plan input | Same canonical hash and ordered changes | 01 |
| T04 | Changed observed state or policy before apply | Stale plan refused; no mutation | 01/03 |
| T05 | Unknown/unowned service, port or Caddy route | Conflict reported; no overwrite/uninstall | 02 |
| T06 | Rootless fixture restart and full VPS reboot | App resumes with expected status; no Brine process required | 02 |
| T07 | Healthy release | Image digest confirmed; route and direct health pass | 03 |
| T08 | Health check failure/new image never starts | Old release restored or actionable recovery state; no false success | 03 |
| T09 | SSH disconnect or CLI crash mid-operation | Operation persists; status/reconcile identifies actual state | 03 |
| T10 | Two concurrent applies on same target | One wins lock; other cleanly refused | 03 |
| T11 | Caddy reload failure or unexpected config drift | Prior Caddy config preserved; no unrelated site changed | 02/03 |
| T12 | One web app owns a persistent SQLite file | App release replacement and reboot preserve data | 04 |
| T13 | Live data replicated to R2 | Isolated restore integrity check + known write verified | 04 |
| T14 | R2 outage, missing credentials, expired secret | Deployment/backup health says degraded; no misleading backup success | 04 |
| T15 | Prevent duplicate replicator on same destination | Second replicator refused before data corruption | 04 |
| T16 | Rollback after schema-breaking migration | Automatic rewind refused; recovery-required state | 03/04 |
| T17 | Malicious app name, domain, environment or plan ID | No argv/shell injection, path escape or arbitrary admin command | 00-04 |
| T18 | Unprivileged agent attempts removal/teardown | Policy boundary refuses independently of CLI UI flags | 06 |
| T19 | Operator performs real R2 restore drill to disposable destination | Restore procedure and resulting DB independently verified | 04/06 |
| T20 | Machine/human parity | Same operation ID and status across JSON CLI/TUI | 05 |
| T21 | Limited disk space, killed process, partial write | Safe recoverable state; no loss of previous release/config | 03/06 |
| T22 | App data cleanup request | Data retained by default; destructive operation unavailable in v1 | 03/06 |
| T23 | Secrets in logs, plans, TUI or JSON errors | Redaction tests and audit of subprocess output paths | 00-06 |
| T24 | Release binary from fresh environment | Go tests, vet, build and version metadata pass | 06 |

## Required gate before real personal data

Record **the exact host distribution, Podman/Caddy/systemd/Litestream versions**, release commit and image digest; verify T05-T16, T18-T19, and T21-T23 on an authorized test target. Run at least one power/reboot-style recovery exercise and one restore from actual R2; simulated success is insufficient.

Document remaining limits prominently: a single server is not high availability; accepted downtime is not zero downtime; Litestream is asynchronous, not a guarantee of no data loss; an agent with direct SSH/sudo/raw runtime privileges can bypass policy.

## Repeatability

Each integration scenario must have a fixture, setup, expected behavior, teardown limited to **resources created by that fixture**, and captured redacted evidence. Never run a blanket `podman system prune`, delete unrelated volumes, flush firewall state or remove another app as part of test cleanup.
