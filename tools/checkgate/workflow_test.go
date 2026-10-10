package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCIRequiresEveryJobAndPinnedActions(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	comparison := "--new-from-merge-base= --new-from-rev=${{ steps.lint-base.outputs.sha }}"
	if !strings.Contains(workflow, comparison) || !strings.Contains(workflow, `sha=$(./scripts/lint-base.sh "$EVENT_NAME" "$PUSH_BASE")`) {
		t.Fatal("lint must select a base consistent with the checked-out synthetic merge")
	}
	parts := strings.SplitN(workflow, "\njobs:\n", 2)
	if len(parts) != 2 {
		t.Fatal("missing CI jobs")
	}
	gate := strings.SplitN(parts[1], "  gate:\n", 2)
	if len(gate) != 2 || !strings.Contains(gate[1], "if: always()") {
		t.Fatal("the final gate must run even after a skipped or failed job")
	}
	needs := regexp.MustCompile(`(?m)^    needs: \[([^]]+)\]$`).FindStringSubmatch(gate[1])
	if len(needs) != 2 {
		t.Fatal("the final gate must list its required jobs")
	}
	required := make(map[string]bool)
	for _, name := range strings.Split(needs[1], ",") {
		required[strings.TrimSpace(name)] = true
	}
	jobs := regexp.MustCompile(`(?m)^  ([a-zA-Z0-9_-]+):$`).FindAllStringSubmatch(parts[1], -1)
	for _, job := range jobs {
		name := job[1]
		if name == "gate" {
			continue
		}
		if !required[name] {
			t.Errorf("CI job %q is missing from Complete CI gate", name)
		}
		if !strings.Contains(gate[1], "needs."+name+".result") {
			t.Errorf("the gate must check job %q's result", name)
		}
		delete(required, name)
	}
	if len(required) != 0 {
		t.Errorf("the gate names nonexistent jobs: %v", required)
	}
	pin := regexp.MustCompile(`^[^@ ]+@[0-9a-f]{40}$`)
	for _, line := range strings.Split(workflow, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- uses: ") {
			continue
		}
		reference := strings.Fields(strings.TrimPrefix(line, "- uses: "))[0]
		if !strings.HasPrefix(reference, "./") && !pin.MatchString(reference) {
			t.Errorf("action must use a full commit SHA: %s", reference)
		}
	}
}

func TestCIGateRejectsEveryUnsuccessfulResult(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(data), "  gate:\n", 2)
	if len(parts) != 2 {
		t.Fatal("missing final gate")
	}
	parts = strings.SplitN(parts[1], "        run: |\n", 2)
	if len(parts) != 2 {
		t.Fatal("missing gate command")
	}
	command := parts[1]
	jobs := []string{"TEST", "LINT", "STRUCTURE", "VULNERABILITY"}
	run := func(t *testing.T, changed, result string, wantSuccess bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", command) // #nosec G204 -- Executes this checkout's reviewed CI gate, never runtime input.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		for _, job := range jobs {
			value := "success"
			if job == changed {
				value = result
			}
			cmd.Env = append(cmd.Env, job+"="+value)
		}
		err := cmd.Run()
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if wantSuccess && err != nil {
			t.Fatalf("successful prerequisites rejected: %v", err)
		}
		if !wantSuccess {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				t.Fatalf("%s=%q must fail the gate: %v", changed, result, err)
			}
		}
	}
	t.Run("all successful", func(t *testing.T) { run(t, "", "", true) })
	for _, job := range jobs {
		for _, result := range []string{"failure", "cancelled", "skipped", ""} {
			t.Run(job+"/"+result, func(t *testing.T) { run(t, job, result, false) })
		}
	}
}

func TestLintBaseUsesSyntheticMergeParent(t *testing.T) {
	root := t.TempDir()
	script, err := filepath.Abs("../../scripts/lint-base.sh")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- Fixed git and repository-script commands in an isolated test checkout.
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	run("git", "init", "-b", "main")
	run("git", "commit", "--allow-empty", "-m", "event base")
	stale := run("git", "rev-parse", "HEAD")
	run("git", "switch", "-c", "feature")
	run("git", "commit", "--allow-empty", "-m", "feature change")
	run("git", "switch", "main")
	run("git", "commit", "--allow-empty", "-m", "main advanced")
	main := run("git", "rev-parse", "HEAD")
	run("git", "merge", "--no-ff", "feature", "-m", "synthetic merge")
	if got := run("bash", script, "pull_request", stale); got != main {
		t.Fatalf("merge checkout baseline = %s, want current main %s, not event base %s", got, main, stale)
	}
	if got := run("bash", script, "push", stale); got != stale {
		t.Fatalf("main push must compare its previous SHA: %s", got)
	}
}

func TestDeadcodeLoadsUnimportedPackages(t *testing.T) {
	data, err := os.ReadFile("../../scripts/check.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"$bin" -json ./...`) {
		t.Fatal("deadcode must load all packages, not only CLI imports")
	}
}
