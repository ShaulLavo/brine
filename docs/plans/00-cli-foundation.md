# Phase 00: CLI foundation

**Goal:** preserve a pleasant human CLI while making agent operations deterministic and testable. Existing scaffold is not a finished contract. **No host changes in this phase.**

### Tasks

- [x] **P00-01** Refactor Cobra command construction to accept injected input/output/error writers, context, and service interfaces. Keep `main` minimal. Unit-test commands without spawning the binary.
  - Evidence: `internal/cli/commands_test.go` covers human and JSON version/doctor output, injected lookup, TUI flag refusal, context and stream forwarding, independent command trees, and unchanged service errors. Baseline and refactored binaries produced byte-identical output for version/doctor and TUI flag refusal.
  - Review regression: `cmd/brine/main_linux_test.go` runs the real binary under a PTY with stdin redirected to `/dev/null`, verifies the welcome screen, sends `q`, and requires exit 0. In-process tests also cover parser/help streams and explicitly injected TUI input.
- [x] **P00-02** Define the versioned response/error envelope and typed domain errors. Ensure `--json` prints exactly one JSON response on success **and failure**; errors include safe codes, never secrets, and stderr has diagnostics only. Document and test exit categories in CONTRACTS.
  - Evidence: `internal/cli/envelope_test.go` covers T01, one-object success and failure snapshots, help, cancellation, and T23 error redaction. `internal/result/result_test.go` checks every error category and the documented code table. `cmd/brine/main_test.go` checks all exit statuses through the process entry function; binary tests cover version, doctor, parser failures, TUI refusal, and SIGINT exit 130.
- [ ] **P00-03** Specify `--no-input`, terminal detection and optional `--jsonl` behavior. Reject machine flags on TUI; reject prompts in noninteractive mode. Use CLI integration tests for stdout/stderr/exit codes.
- [ ] **P00-04** Refactor the PATH-only doctor into dependency checks with machine-friendly `name`, `available`, `version`, and `reason` fields. Do not confuse a local PATH check with remote host readiness.
- [ ] **P00-05** Migrate Bubble Tea, Lip Gloss and Bubbles to their stable v2 releases (`charm.land/...`) in one **coordinated** change. Add a modest themed reusable UI shell and terminal-size handling; no dummy deploy success screens.
- [x] **P00-06** CI: gofmt, tests, vet, build, race tests, module tidiness, whitespace checks, and machine-output snapshots are implemented. Avoid automatically updating all dependencies.

### Exit gate

`brine version --json --no-input` and `brine doctor --json --no-input` can be parsed without stripping ANSI codes; machine failures have stable schemas and tested exit codes. Invalid TUI/machine flag combinations fail. No dependency checks mutate infrastructure.

**Evidence:** automated tests for T01, T17 and T23; `go test ./...`, `go vet ./...`, and a fresh build. Finish this before interpreting later CLI names as implemented.
