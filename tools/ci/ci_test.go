package ci

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type step struct {
	ContinueOnError bool `yaml:"continue-on-error"`
	Uses            string
	Run             string
	If              string
	Env             map[string]string
	With            map[string]string
}

type job struct {
	ContinueOnError bool `yaml:"continue-on-error"`
	Env             map[string]string
	Permissions     map[string]string
	Name            string
	If              string
	Needs           yaml.Node
	Steps           []step
	Timeout         int `yaml:"timeout-minutes"`
	Strategy        struct {
		FailFast *bool `yaml:"fail-fast"`
		Matrix   struct{ Shard []string }
	}
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
		"cli-pair":        {"Linux CLI binaries", `bash scripts/build-cli-pair.sh "$RUNNER_TEMP/brine-cli-pair"`, "cli-pair"},
		"test":            {"Tests, types and Mac client", "./scripts/check.sh test-normal", "test"},
		"race":            {"Race tests (${{ matrix.shard }})", "./scripts/check.sh test-race-${{ matrix.shard }}", "race-${{ matrix.shard }}"},
		"race-host-build": {"Race tests (host build)", `./scripts/check.sh test-race-host-build "$RUNNER_TEMP/host-race"`, "race-host"},
		"lint":            {"New-code static checks", "golangci-lint run --config ../../.golangci.yml --new-from-merge-base= --new-from-rev= ./...", "lint"},
		"structure":       {"Authority, dead code and clones", "./scripts/check.sh structure", "structure"},
		"vulnerability":   {"Vulnerability scan", "./scripts/check.sh vulnerability", "vulnerability"},
	}
	for id, want := range expected {
		checkJob(t, id, workflow.Jobs[id], want.name, want.run, want.role)
	}
	if workflow.Jobs["test"].Steps[2].Run != "bash scripts/test-ci.sh" {
		t.Fatal("CI harness tests must run before the normal suite")
	}
	race := workflow.Jobs["race"]
	checkRaceLimits(t, race)
	checkHostRaceWorkflow(t, workflow.Jobs)
	if !reflect.DeepEqual(race.Strategy.Matrix.Shard, []string{"store", "rest"}) {
		t.Fatal("store and rest race shards must remain present")
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
	if gate.Name != "Complete CI gate" || gate.If != "always()" || !reflect.DeepEqual(needs, []string{"changes", "test", "race", "race-host-build", "race-host", "lint", "structure", "vulnerability", "cli-pair", "helpers"}) {
		t.Fatal("complete blocking gate changed")
	}
	for key, id := range map[string]string{"CHANGES": "changes", "TEST": "test", "RACE": "race", "RACE_HOST_BUILD": "race-host-build", "RACE_HOST": "race-host", "LINT": "lint", "STRUCTURE": "structure", "VULNERABILITY": "vulnerability", "CLI_PAIR": "cli-pair", "HELPERS": "helpers"} {
		if gate.Steps[0].Env[key] != "${{ needs."+id+".result }}" || !strings.Contains(gate.Steps[0].Run, "$"+key+" == success") {
			t.Fatalf("gate must require %s to succeed", id)
		}
	}
}

func checkJob(t *testing.T, id string, j job, name, run, role string) {
	t.Helper()
	if j.Name != name || j.If != "" && id != "race" && id != "race-host-build" {
		t.Fatalf("%s name or condition changed: %+v", id, j)
	}
	var foundRun, foundCache bool
	for _, s := range j.Steps {
		foundRun = foundRun || s.Run == run
		foundCache = foundCache || s.Uses == "./.github/actions/go-ci" && s.With["role"] == role
		if id != "race" && id != "race-host-build" && s.If != "" {
			t.Fatalf("%s must always run every step", id)
		}
	}
	if !foundRun || !foundCache {
		t.Fatalf("%s lost its command or role cache", id)
	}
}

func checkRaceLimits(t *testing.T, j job) {
	t.Helper()
	if j.Timeout != 45 || j.Strategy.FailFast == nil || *j.Strategy.FailFast {
		t.Fatal("every race shard needs the 45-minute deadline and fail-fast disabled")
	}
}

func checkHostRaceWorkflow(t *testing.T, jobs map[string]job) {
	t.Helper()
	build, run := jobs["race-host-build"], jobs["race-host"]
	if build.If != "always()" || build.Needs.Value != "changes" || build.Timeout != 45 {
		t.Fatal("host build must retain its deadline and run on classification failure")
	}
	artifact := build.Steps[3]
	if artifact.Uses != "actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02" || artifact.With["name"] != "host-race-${{ github.sha }}" || artifact.With["path"] != "${{ runner.temp }}/host-race/" || artifact.With["if-no-files-found"] != "error" {
		t.Fatal("host race binary artifact lost its pinned upload or provenance")
	}
	var needs []string
	if err := run.Needs.Decode(&needs); err != nil {
		t.Fatal(err)
	}
	if run.Name != "Race tests (host ${{ matrix.shard }})" || run.If != "always() && needs.race-host-build.result == 'success'" || !reflect.DeepEqual(needs, []string{"changes", "race-host-build"}) {
		t.Fatal("host consumers must depend on a successful binary build")
	}
	checkRaceLimits(t, run)
	command := exec.CommandContext(t.Context(), "bash", "../../scripts/check.sh", "test-race-host-shards")
	command.Env = append(os.Environ(), "BRINE_CHECK_OUTPUT="+t.TempDir())
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != "["+strings.Join(run.Strategy.Matrix.Shard, ",")+"]" {
		t.Fatalf("host matrix must match the script's complete shard list: %s, %v", output, err)
	}
	for _, s := range run.Steps {
		if s.Uses == "./.github/actions/go-ci" || strings.HasPrefix(s.Run, "go ") {
			t.Fatal("host consumers must reuse the binary, not compile it again")
		}
	}
	download := run.Steps[1]
	if download.Uses != "actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093" || download.With["name"] != artifact.With["name"] || download.With["path"] != "${{ runner.temp }}/host-race" {
		t.Fatal("host consumers must download the exact build artifact")
	}
	if run.Steps[2].Run != `chmod +x "$RUNNER_TEMP/host-race/host.test" && ./scripts/check.sh test-race-host-shard ${{ matrix.shard }} "$RUNNER_TEMP/host-race"` {
		t.Fatal("host consumers must execute the prebuilt shard")
	}
	checkHostRaceConditions(t, build.Steps)
	checkHostRaceConditions(t, run.Steps)
}

func checkHostRaceConditions(t *testing.T, steps []step) {
	t.Helper()
	for _, s := range steps[1 : len(steps)-1] {
		if s.If != "needs.changes.outputs.scope != 'docs'" {
			t.Fatal("host race steps must run unless classification proves docs-only")
		}
	}
	if steps[len(steps)-1].If != "needs.changes.outputs.scope == 'docs'" {
		t.Fatal("only documentation changes may omit host race execution")
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

func TestPureHelperWorkflow(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Permissions map[string]string
		Jobs        map[string]job
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	helpers := workflow.Jobs["helpers"]
	want := []step{
		{Uses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", With: map[string]string{"persist-credentials": "false"}},
		{Uses: "oven-sh/setup-bun@0c5077e51419868618aeaa5fe8019c62421857d6", With: map[string]string{"bun-version": "1.4.4"}},
		{Run: "./scripts/check.sh test-helpers"},
	}
	if helpers.Name != "Publication helper tests" || helpers.If != "" || helpers.ContinueOnError || len(helpers.Env) != 0 || len(helpers.Permissions) != 0 || !reflect.DeepEqual(helpers.Steps, want) {
		t.Fatal("mandatory helper job must only check out source, set up pinned Bun, and run the pure suite")
	}
	if !reflect.DeepEqual(workflow.Permissions, map[string]string{"contents": "read"}) {
		t.Fatal("CI must not gain publication permissions")
	}
	publication, err := os.ReadFile("../../.github/workflows/udp-fixture.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(publication), `bun-version: "1.4.4"`) || !strings.Contains(string(publication), "run: bun test tools/ci/udp-fixture") {
		t.Fatal("merge-time Bun pin and suite must match publication without removing its checks")
	}
}

func TestWorkerSemanticRuntime(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct{ Jobs map[string]job }
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["test"].Steps
	setup := step{Uses: "actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1", With: map[string]string{"node-version": "26.12.1", "package-manager-cache": "false"}}
	if len(steps) < 4 || !reflect.DeepEqual(steps[3], setup) {
		t.Fatal("normal tests must explicitly set up pinned Node before running Worker semantics")
	}
}
