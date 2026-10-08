# Phase 00: CLI foundation

**Goal:** preserve a pleasant human CLI while making agent operations deterministic and testable. Existing scaffold is not a finished contract. **No host changes in this phase.**

### Tasks

- [ ] **P00-01** Refactor Cobra command construction to accept injected input/output/error writers, context, and service interfaces. Keep `main` minimal. Unit-test commands without spawning the binary.
- [ ] **P00-02** Define the versioned response/error envelope and typed domain errors. Ensure `--json` prints exactly one JSON response on success **and failure**; errors include safe codes, never secrets, and stderr has diagnostics only. Document and test exit categories in CONTRACTS.
- [ ] **P00-03** Specify `--no-input`, terminal detection and optional `--jsonl` behavior. Reject machine flags on TUI; reject prompts in noninteractive mode. Use CLI integration tests for stdout/stderr/exit codes.
- [ ] **P00-04** Refactor the PATH-only doctor into dependency checks with machine-friendly `name`, `available`, `version`, and `reason` fields. Do not confuse a local PATH check with remote host readiness.
- [ ] **P00-05** Review Charm release compatibility. Migrate Bubble Tea/Lip Gloss/Bubbles as a **coordinated** v2 change if compatible, otherwise record why they remain pinned. Add a modest themed reusable UI shell and terminal-size handling; no dummy deploy success screens.
- [ ] **P00-06** CI: gofmt, tests, vet, build, relevant race tests, and snapshot tests for machine output. Avoid automatically updating all dependencies.

### Exit gate

`brine version --json --no-input` and `brine doctor --json --no-input` can be parsed without stripping ANSI codes; machine failures have stable schemas and tested exit codes. Invalid TUI/machine flag combinations fail. No dependency checks mutate infrastructure.

**Evidence:** automated tests for T01, T17 and T23; `go test ./...`, `go vet ./...`, and a fresh build. Finish this before interpreting later CLI names as implemented.
