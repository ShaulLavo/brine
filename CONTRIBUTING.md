# Contributing to Brine

Read [AGENTS.md](AGENTS.md), the [decisions](docs/DECISIONS.md) and the relevant [phase plan](docs/plans/README.md) before changing behavior. Local tests use fakes. They do not authorize contacting or modifying a host.

## Install tools and hooks

The repository pins Go 1.27.2, golangci-lint 2.14.0 and Lefthook 2.1.15 in `mise.toml`. The module still declares Go 1.26 as its minimum. With [mise](https://mise.jdx.dev/) installed:

```sh
mise trust
mise install
mise exec -- lefthook install
mise exec -- ./scripts/check.sh
```

Without mise, install those exact versions and run `./scripts/check.sh`. The script installs deadcode from `golang.org/x/tools` v0.51.0 and govulncheck v1.7.0 into versioned cache directories. It also installs golangci-lint 2.14.0 if that version is not on PATH. First use needs network access and is slower than a warm check.

On the development workstation, keep tool downloads and build caches on the data drive:

```sh
export BRINE_TOOLS=/work/cache/brine-tools
export GOCACHE=/work/cache/go-build
mkdir -p .tmp
export GOTMPDIR="$PWD/.tmp" TMPDIR="$PWD/.tmp"
```

Do not use that path on a machine without a mounted `/work`. Elsewhere the tool cache defaults to `$XDG_CACHE_HOME/brine-tools`, or `$HOME/.cache/brine-tools`.

## Run the same checks as CI

`./scripts/check.sh` runs formatting, whitespace, module tidiness, vet, Mac arm64 vet, ordinary and race tests, build, static checks, authority rules, dead code, clones and vulnerabilities. Go compilation supplies the type check. Tests run one package at a time because disk and subprocess fixtures have tight timing boundaries. Their 30-minute ceiling accommodates a loaded development host; production deadlines do not change.

You can run one group:

```sh
./scripts/check.sh test
./scripts/check.sh lint
./scripts/check.sh structure
./scripts/check.sh vulnerability
./scripts/check.sh fast
```

Full lint compares against the merge base with `origin/main`. Fetch that ref before checking a feature branch. Set `BRINE_BASE` to a specific commit to choose another comparison. CI compares synthetic pull-request merge checkouts with their first parent, the main commit actually merged into that checkout. Main pushes compare with the previous main SHA. Comparing a main push with its own HEAD would hide every new finding.

The final **Complete CI gate** requires tests, static checks, structure checks and the vulnerability scan to succeed. Select that check for branch protection. All workflow actions use commit SHA pins.

## Pre-commit checks

Lefthook formats staged Go files and stages those fixes. It then checks the whole tree for authority violations and clones, and runs the configured golangci linters on changed packages against `HEAD`. The package loader type-checks those packages. This keeps the hook narrower than full tests and reachability analysis. It does not replace CI's whole-tree lint, deadcode or vulnerability scan.

The hook needs warm tool and Go caches for the five-second target. A changed large package, cold compiler cache or heavily loaded host can exceed it. Run the full script before opening a pull request even if the hook passes.

## Rules with an executable check

`tools/checkgate` parses Go syntax using the standard library. No new runtime dependency is needed. It checks all Go files, including files for other operating systems:

- Only `cmd`, `internal/cli` and `internal/ui` may import Cobra or Charm.
- Only `internal/localexec` and test files may import or use `os/exec`. New uses in a file with an existing import still fail.
- Production randomness must use `crypto/rand`, not either `math/rand` package. Tests may use deterministic random data.
- `InsecureSkipVerify` is forbidden, including in tests. Existing integration-fixture use is recorded for cleanup, not endorsed.
- Production `time.Sleep` references fail. There are no allowed production sleep helpers today. Prefer a timer selected with the operation context.
- Production dot imports fail because they hide package authority from both readers and syntax checks.

The linters add cancellation propagation, context-bearing network calls, nil-error branch checks and wrapped-error matching to Mesh's existing resource, error and security checks. Gocritic and revive use small correctness-only rule sets. Naming and complexity scores do not belong in this gate.

## Existing findings and deliberate entry points

Do not repair unrelated packages just to make lint output empty. Static checks accept only findings that predate the comparison commit. The other checks use reviewed JSON files in `tools/baselines`:

- `rules.json` records existing authority findings by rule, file, function and syntax hash.
- `deadcode-linux.json` records existing unreachable qualified symbols in the Linux call graph. Analysis loads `./...`, including packages not imported by the executable, without test roots. Only production main packages supply reachability roots, so tests cannot make unused production code appear live. Mac client compilation is checked by vet, not by interpreting Linux-only host operations as Mac dead code.
- `deadcode-allowlist.json` gives a reason for each deliberately unreachable test adapter. It has no wildcard exemptions.
- `duplicates.json` records directed clone fragments. Dupl checks production Go at 100 syntax nodes. The baseline keys include source tokens, the file and the counterpart file, not line numbers. Moving a clone does not fail; adding another copy or changing its tokens does.

Counts matter. Copying an identical finding within the same file still exceeds the old allowance. Missing, malformed or null baseline/report data fails. Removed findings do not require a simultaneous baseline edit, so concurrent cleanup lanes can merge independently. Delete obsolete entries when the cleanup settles.

Reports from the structure tools go to `.tmp/checks`. For a complete static-check cleanup inventory, run:

```sh
golangci-lint run --new-from-merge-base= --new=false --issues-exit-code=0 \
  --output.json.path=.tmp/checks/lint-existing.json ./...
```

New findings should be fixed. When a deliberate entry point or clone is necessary, edit its specific allowance in a reviewed change and explain why. `checkgate --write-baseline` exists for explicit initial capture and reviewed maintenance only. Neither CI nor the hook writes baselines.

## Limits

These checks do not prove a deployment, restore, authorization boundary or hardware recovery drill. They do not scan private host state. The deployment and restore gates in [TEST_MATRIX.md](docs/TEST_MATRIX.md) still need their specified physical evidence.
