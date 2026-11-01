package ci

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type step struct {
	Uses string
	Run  string
	If   string
	With map[string]string
}

type job struct {
	Name     string
	If       string
	Needs    yaml.Node
	Steps    []step
	Strategy struct{ Matrix struct{ Shard []string } }
}

func TestCIWorkflow(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct{ Jobs map[string]job }
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	expected := map[string]struct{ name, run, role string }{
		"cli-pair":      {"Linux CLI binaries", `bash scripts/build-cli-pair.sh "$RUNNER_TEMP/brine-cli-pair"`, "cli-pair"},
		"test":          {"Tests, types and Mac client", "./scripts/check.sh test-normal", "test"},
		"race":          {"Race tests (${{ matrix.shard }})", "./scripts/check.sh test-race-${{ matrix.shard }}", "race-${{ matrix.shard }}"},
		"lint":          {"New-code static checks", "golangci-lint run --config ../../.golangci.yml --new-from-merge-base= --new-from-rev= ./...", "lint"},
		"structure":     {"Authority, dead code and clones", "./scripts/check.sh structure", "structure"},
		"vulnerability": {"Vulnerability scan", "./scripts/check.sh vulnerability", "vulnerability"},
	}
	for id, want := range expected {
		checkJob(t, id, workflow.Jobs[id], want.name, want.run, want.role)
	}
	if workflow.Jobs["test"].Steps[2].Run != "bash scripts/test-ci.sh" {
		t.Fatal("CI harness tests must run before the normal suite")
	}
	race := workflow.Jobs["race"]
	if !reflect.DeepEqual(race.Strategy.Matrix.Shard, []string{"store", "host", "rest"}) {
		t.Fatal("all existing race shards must remain present")
	}
	if race.If != "always()" || race.Needs.Value != "changes" {
		t.Fatal("classification failure must still run race tests")
	}
	for _, s := range race.Steps {
		if (s.Uses == "./.github/actions/go-ci" || strings.HasPrefix(s.Run, "./scripts/check.sh")) && s.If != "needs.changes.outputs.scope != 'docs'" {
			t.Fatalf("race tests must default to running: %+v", s)
		}
	}
	pair := workflow.Jobs["cli-pair"].Steps
	if pair[0].With["ref"] != "${{ github.event.pull_request.head.sha || github.sha }}" || pair[3].With["name"] != "brine-cli-${{ github.event.pull_request.head.sha || github.sha }}" || pair[3].With["path"] != "${{ runner.temp }}/brine-cli-pair/" || pair[3].With["if-no-files-found"] != "error" {
		t.Fatal("CLI artifact provenance interface changed")
	}
	lint := workflow.Jobs["lint"].Steps[3]
	if lint.With["skip-cache"] != "true" || lint.With["version"] != "v2.14.0" || lint.With["args"] != "--new-from-merge-base= --new-from-rev=${{ steps.lint-base.outputs.sha }}" {
		t.Fatal("lint command or analyzer cache ownership changed")
	}
	gate := workflow.Jobs["gate"]
	var needs []string
	if err := gate.Needs.Decode(&needs); err != nil {
		t.Fatal(err)
	}
	if gate.Name != "Complete CI gate" || gate.If != "always()" || !reflect.DeepEqual(needs, []string{"changes", "test", "race", "lint", "structure", "vulnerability", "cli-pair"}) {
		t.Fatal("complete blocking gate changed")
	}
}

func checkJob(t *testing.T, id string, j job, name, run, role string) {
	t.Helper()
	if j.Name != name || j.If != "" && id != "race" {
		t.Fatalf("%s name or condition changed: %+v", id, j)
	}
	var foundRun, foundCache bool
	for _, s := range j.Steps {
		foundRun = foundRun || s.Run == run
		foundCache = foundCache || s.Uses == "./.github/actions/go-ci" && s.With["role"] == role
		if id != "race" && s.If != "" {
			t.Fatalf("%s must always run every step", id)
		}
	}
	if !foundRun || !foundCache {
		t.Fatalf("%s lost its command or role cache", id)
	}
}

func TestCICache(t *testing.T) {
	data, err := os.ReadFile("../../.github/actions/go-ci/action.yml")
	if err != nil {
		t.Fatal(err)
	}
	var action struct{ Runs struct{ Steps []step } }
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	setup, paths, cache := action.Runs.Steps[0], action.Runs.Steps[1], action.Runs.Steps[2]
	if setup.With["cache"] != "false" || setup.With["go-version"] != "1.27.3" {
		t.Fatal("setup-go must not compete with per-role caches")
	}
	for _, s := range []string{"go env GOMODCACHE", "go env GOCACHE", "BRINE_TOOLS=", "GOLANGCI_LINT_CACHE=", "GITHUB_ENV"} {
		if !strings.Contains(paths.Run, s) {
			t.Fatalf("cache paths lost %s", s)
		}
	}
	for _, output := range []string{"modules", "build", "tools", "analyzers"} {
		if !strings.Contains(cache.With["path"], "${{ steps.paths.outputs."+output+" }}") {
			t.Fatalf("cache lost %s", output)
		}
	}
	for _, input := range []string{"runner.os", "runner.arch", "inputs.role", "**/go.mod", "**/go.sum", "scripts/check.sh", ".golangci*.yml", ".github/actions/go-ci/action.yml"} {
		if !strings.Contains(cache.With["key"], input) || !strings.Contains(cache.With["restore-keys"], input) {
			t.Fatalf("cache identity lost %s", input)
		}
	}
	if !strings.HasSuffix(cache.With["key"], "-${{ github.sha }}") || strings.Contains(cache.With["restore-keys"], "github.sha") {
		t.Fatal("new commits must refresh their role cache and restore earlier commits")
	}
	if cache.Uses != "actions/cache@0400d5f644dc74513175e3cd8d07132dd4860809" {
		t.Fatal("cache action must stay pinned")
	}
}
