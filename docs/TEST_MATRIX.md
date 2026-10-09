# Brine acceptance and failure test matrix

**This is a test plan, not a report of passing deployments.** No real VPS is authorized by this file. Unit tests use synthetic fixtures and fake adapters; destructive integration tests require an explicitly named disposable target and clean test credentials.

| ID | Scenario | Required evidence | Phase |
| --- | --- | --- | --- |
| T01 | Human and JSON version/doctor output | Valid schema, no ANSI or prompts in machine output | 00 |
| T02 | Invalid config, unknown field, unsafe path/domain/image | Validation refusal; no host or control-plane effects | 01 |
| T03 | Repeated same plan decision facts, including different disk-byte measurements within the same sufficient/insufficient class | Same canonical hash and ordered changes; sampled perturbations of every snapshot decision field change hash material | 01 |
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
| T18 | Agent key attempts an operation outside the allowlist (shell, raw Podman/Caddy, non-Brine software, purge or live restore when policy forbids), invokes the root helper directly, forges an operation record, or substitutes a path/symlink | Dispatcher and root helper each refuse independently of CLI flags and runner-writable state | 06 |
| T19 | Operator performs real R2 restore drill to disposable destination | Restore procedure and resulting DB independently verified | 04/06 |
| T20 | Machine/human parity | Same operation ID and status across JSON CLI/TUI | 05 |
| T21 | Limited disk space, killed process, partial write | Planning below the policy disk minimum or with unobserved disk conflicts with no changes; crossing the minimum changes the hash; safe recoverable state and no loss of previous release/config after runtime failures | 03/06 |
| T22 | App removal, recreate, expiry and purge | P03-09 stateless removal withdraws only owned routes/units after writer fencing, retains release/secret history, releases its port, refuses persistent data, and reconciles each effect boundary. P04-08 removal archives under an immutable ID with a restorable backup set for 30 days; remove-recreate-expire never touches the new app; purge only when policy allows; interrupted archive/purge recovers | 03/04/06 |
| T23 | Secrets in logs, plans, TUI or JSON errors | Redaction tests and audit of subprocess output paths | 00-06 |
| T24 | Release binary from fresh environment | Go tests, vet, build and version metadata pass | 06 |

## Required gate before real personal data

Record **the exact host distribution, Podman/Caddy/systemd/Litestream versions**, release commit and image digest; verify T05-T16, T18-T19, and T21-T23 on an authorized test target. Run at least one power/reboot-style recovery exercise and one restore from actual R2; simulated success is insufficient.

Document remaining limits prominently: a single server is not high availability; accepted downtime is not zero downtime; Litestream is asynchronous, not a guarantee of no data loss; an agent with direct SSH/sudo/raw runtime privileges can bypass policy.

## Repeatability

Each integration scenario must have a fixture, setup, expected behavior, teardown limited to **resources created by that fixture**, and captured redacted evidence. Never run a blanket `podman system prune`, delete unrelated volumes, flush firewall state or remove another app as part of test cleanup.


## P03-09 local evidence (physical host pending)

- T04/T05/T17/T18: removal planner ownership and persistent-unit tests; stale
  connected apply journals `stale_plan` with no effects; hash, marker, symlink
  and mounted-data deletion refusals; mutating dispatch authorization and strict
  app-only argument tests refuse paths, arbitrary units and purge flags.
- T09/T10/T21: `internal/host/remove_crash_linux_test.go` SIGKILLs an actual
  run-op subprocess after each of five durable fake effects, proves it held the
  host lock, verifies dry-run reconciliation changes neither journal nor fake
  host, and converges through a detached recovery receipt. Eighteen unit prefix
  cases cover before/after/journaled boundaries; drift and unknown writers/jobs
  fail closed. Unknown-effect tests journal/read back without blind retries.
- T11: complete Caddy generation settlement tests refuse foreign hash drift and
  adapted routes that still send traffic to the removed application; disk state
  alone is never treated as evidence that an uncertain reload succeeded.
- T20/T22/T23: human/JSON/JSONL removal goldens; connected removal and repeated
  no-op; immutable retirement receipt, retained release history, freed port and
  old-removal/new-head protection. Machine/human output explains D5 retention;
  the executor never calls secret/data deletion adapters.
- Gate: Go tests/race tests, Linux and Darwin arm64 vet, build, gofmt and diff
  checks. Local fakes and SIGKILL are not physical Caddy/Podman/systemd, SSH
  disconnect or reboot evidence. P04-08 archival/restore/expiry/purge and the
  separately authorized physical-host acceptance lane remain pending.

- Review regressions (failing-first): removal after registry/domain revocation,
  including an unrelated revoked app; interrupted no-op completion after
  intent/completed preflight and refusal of changed/unknown absence; spaced/tabbed
  Volume/Mount/ReadWritePaths directives; unified v1-to-v2 migration preserving
  the deploy journal and creating immutable removal receipts. All pass locally.

### Terminal recovery resolution regressions

- T09/T10/T21: exact terminal removal prefix (withdraw completed, stop unknown/
  interrupted) with stopped writer and withdrawn route; dispatcher/store/runner
  resolution retires head/port without replaying withdrawal or stop. An actual
  v2 database migrates to v3 preserving that journal and terminal identity.
- T07/T09: equivalent first-deploy and update installed-candidate readback
  rolls back safely; ambiguous live owned-unit hashes refuse with no effects.
- T09/T10/T21: local run-op SIGKILL at remaining removal deletion, reload and
  retirement effects; read-only preview, host-lock proof, detached reconciliation
  and repeated convergence. This is fake-host evidence, not Pi execution.
- T20/T22: closed resolve CLI/dispatcher protocol, mutating D8 denial and
  acceptance, idempotent creation, active-successor exclusion and immutable
  terminal/source guards. Secret present/absent resolution and interrupted
  successor reconciliation inspect only the named version and never create it.
- Historical-artifact regression: terminal removal with a committed pre-log-policy
  Quadlet hash succeeds after the renderer adds LogDriver/LogOpt. Inspection and
  deletion use the recorded hash, not freshly rendered unit bytes; normal drift
  checks remain enabled. Both new diagnostic rules survive the main merge.

Resolution receipt ancestry: committed/retired evidence held by the original,
intermediate or immediate source is retained across repeated terminal resolution
attempts. Foreign plan/app, non-recovery-required state, unsupported kind,
cyclic and overlong (more than 64 ancestors) chains refuse without host effects.
## P03 imported-route provenance evidence (physical rerun pending)

- T05/T11: `internal/inventory/provenance_test.go` uses the production collector,
  generation filesystem shape and real Caddy v2.6.2 adapter JSON. The source root
  has a global `local_certs` block plus the fixed generation import. Fixtures
  cover an empty generation, one app, a separate operator site, an overlapping
  HTTP site and a catchall. Capture used `caddy adapt --config Caddyfile
  --adapter caddyfile`, with the fixed import redirected only to a local fixture
  generation. Adapter JSON contains only reserved example names and loopback
  upstreams; capture paths and diagnostics are not included.
- A create plan followed by a committed-release environment-update plan passes
  using collected routing facts, not pre-attributed routing snapshots. Before
  the fix, the update test reports `artifact_drift caddy.live`, `domain_owned
  domains` and `unknown_facts live_caddy_files.domains`.
- Root-owned domains, overlapping owners and catchalls still refuse with
  `domain_owned`. Changed rendered bytes, failed or unmatched adaptation,
  unknown generation and disk/live disagreement never authorize an update.
  Complete disk-adapted/live JSON equality remains a prerequisite.
- These are local collector/planner and real-adapter fixtures, not a successful
  Pi update, rollback, removal, recovery or R2 restore. Physical reruns belong
  to the separately authorized host lane.

- Review regression: real Caddy v2.6.2 adapts an unused snippet containing the
  generation import plus an equivalent root-owned site to the same JSON as a
  real imported site. The failing-first collector/planner test proves route
  equality alone cannot establish import execution. Reusing D4's root lexer
  now leaves provenance unknown and refuses the update. Source-context tests
  also refuse imports in site blocks, named routes, other files and roots with
  additional imports.

- Diagnostic consumer regression: `internal/diagnose/collector_provenance_test.go`
  feeds the production collector directly into the diagnostic reader. Healthy
  attributed files now prove the live generation and route presence. Every
  selected file must still have known live evidence; missing sources, wrong app
  attribution or unknown domains keep `live_generation_unproven`. Opaque names
  remain valid only for currently emitted unattributed sources, not as a
  substitute for an app association on filename-shaped sources. The diagnostic
  change is confined to `liveGeneration`; log collection paths are unchanged.

- The imported-file boundary uses the same pinned Caddy lexer, including quoted
  import directive names. A real-adapter probe confirmed that a quoted import
  in another unused snippet contributes no routes while the executed app import
  still produces equivalent JSON. Its failing-first collector regression now
  refuses all attribution. Lexer tests distinguish quoted directives from
  harmless response strings and comments; environment expansion and malformed
  source tokens also stay unknown.
