package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestAgentExampleCommands(t *testing.T) {
	for _, file := range []string{"../../docs/guides/agents.md", "../../examples/automation/restart.sh"} {
		checkAgentExampleFile(t, file)
	}
}

func checkAgentExampleFile(t *testing.T, file string) {
	t.Helper()
	raw, err := os.ReadFile(file) // #nosec G304 -- fixed repository paths.
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		checkAgentExampleLine(t, line)
	}
}

func agentExampleArgs(line string) ([]string, bool) {
	if strings.HasPrefix(line, "brine ") {
		return strings.Fields(strings.TrimPrefix(line, "brine ")), true
	}
	const invocation = `"$BRINE" `
	start := strings.Index(line, invocation)
	if start < 0 {
		return nil, false
	}
	return strings.Fields(line[start+len(invocation):]), true
}

func checkAgentExampleLine(t *testing.T, line string) {
	t.Helper()
	args, found := agentExampleArgs(line)
	if !found {
		return
	}
	if len(args) == 0 {
		t.Fatalf("empty command: %s", line)
	}
	root := NewRootCommand(Dependencies{Context: context.Background()})
	cmd, _, err := root.Find(args)
	if err != nil || cmd == root {
		t.Fatalf("unknown command in %q: %v", line, err)
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		name := strings.TrimRight(strings.SplitN(strings.TrimPrefix(arg, "--"), "=", 2)[0], `)";`)
		if cmd.Flags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil {
			t.Fatalf("unknown flag %s in %q", name, line)
		}
	}
}

func TestAgentExample(t *testing.T) {
	for _, tool := range []string{"sh", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s required: %v", tool, err)
		}
	}
	for _, scenario := range []string{"success", "failure", "unknown", "acceptance-unknown", "no-op", "conflict"} {
		t.Run(scenario, func(t *testing.T) { runAgentExample(t, scenario) })
	}
}

func runAgentExample(t *testing.T, scenario string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	wrapper := filepath.Join(dir, "brine")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestAgentExampleHelper$ -- \"$@\"\n", binary)), 0700); err != nil { // #nosec G306 -- the private test wrapper must execute.
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "../../examples/automation/restart.sh", "demo", "fixture", "/fixture/config", "fixed-key")
	cmd.Env = append(os.Environ(), "BRINE="+wrapper, "AGENT_HELPER=1", "AGENT_SCENARIO="+scenario, "AGENT_LOG="+log, "POLL_INTERVAL=0")
	output, err := cmd.CombinedOutput()
	if (err == nil) != (scenario == "success") {
		t.Fatalf("scenario %s: %v: %s", scenario, err, output)
	}
	raw, err := os.ReadFile(log) // #nosec G304 -- path is inside t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	if scenario == "no-op" || scenario == "conflict" {
		if calls != "lifecycle\n" {
			t.Fatalf("non-applyable plan was applied: %s", calls)
		}
		return
	}
	if scenario == "acceptance-unknown" {
		checkAgentAcceptanceUnknown(t, calls, string(output))
		return
	}
	want := "lifecycle\napply fixed-key\noperation 0\noperation 1\n"
	if scenario == "failure" {
		want += "diagnose\n"
	}
	if calls != want {
		t.Fatalf("unexpected calls: %s output: %s", calls, output)
	}
	if scenario == "unknown" && (!strings.Contains(string(output), "reconcile --dry-run") || !strings.Contains(string(output), "/fixture/config")) {
		t.Fatalf("missing recovery guidance: %s", output)
	}
}

func checkAgentAcceptanceUnknown(t *testing.T, calls, output string) {
	t.Helper()
	if calls != "lifecycle\napply fixed-key\ndiagnose\n" {
		t.Fatalf("uncertain acceptance retried or polled: %s", calls)
	}
	if !strings.Contains(output, "plan_id=sha256:"+strings.Repeat("a", 64)) || !strings.Contains(output, "idempotency_key=fixed-key") {
		t.Fatalf("uncertain acceptance lost the original plan or key: %s", output)
	}
}

func TestAgentExampleHelper(t *testing.T) {
	if os.Getenv("AGENT_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	deps := Dependencies{Context: context.Background(), Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, LoadOperationTarget: func(dir, target string) (transport.Target, error) {
		if target != "fixture" || dir != "/fixture/config" {
			return transport.Target{}, fmt.Errorf("lost target/config: %s %s", target, dir)
		}
		return transport.Target{Name: target}, nil
	}}
	deps.OperationClient = callFunc(agentExampleCall)
	if err := Execute(deps, args); err != nil {
		os.Exit(result.ExitCode(err))
	}
	os.Exit(0)
}

func agentExampleLog(line string) error {
	f, err := os.OpenFile(os.Getenv("AGENT_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600) // #nosec G703 -- helper receives a t.TempDir path.
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(f, line)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func agentExampleCall(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
	id := "sha256:" + strings.Repeat("a", 64)
	switch req.Op {
	case "lifecycle":
		if err := agentExampleLog("lifecycle"); err != nil {
			return result.Envelope{}, err
		}
		kind := plan.Update
		if os.Getenv("AGENT_SCENARIO") == "no-op" {
			kind = plan.NoOp
		}
		if os.Getenv("AGENT_SCENARIO") == "conflict" {
			kind = plan.Conflict
		}
		return result.Success("host", apps.ConfigPlan{PlanID: id, Kind: kind, Conflicts: []plan.Diagnostic{}}), nil
	case "apply":
		var a dispatch.ApplyArgs
		_ = json.Unmarshal(req.Args, &a)
		if a.PlanID != id {
			return result.Envelope{}, fmt.Errorf("unexpected plan ID %q", a.PlanID)
		}
		if err := agentExampleLog("apply " + a.IdempotencyKey); err != nil {
			return result.Envelope{}, err
		}
		if os.Getenv("AGENT_SCENARIO") == "acceptance-unknown" {
			return result.Envelope{}, result.New(result.TransportFailure, nil)
		}
		return result.Success("host", jobs.Accepted{Status: "accepted", OperationID: "op1"}), nil
	case "operation":
		var a dispatch.OperationArgs
		_ = json.Unmarshal(req.Args, &a)
		if a.OperationID != "op1" {
			return result.Envelope{}, fmt.Errorf("unexpected operation ID %q", a.OperationID)
		}
		if err := agentExampleLog(fmt.Sprint("operation ", a.AfterCursor)); err != nil {
			return result.Envelope{}, err
		}
		state := ops.Queued
		if a.AfterCursor > 0 {
			state = map[string]ops.State{"success": ops.Succeeded, "failure": ops.Failed, "unknown": ops.LaunchUnknown}[os.Getenv("AGENT_SCENARIO")]
		}
		seq := a.AfterCursor + 1
		return result.Success("host", jobs.Status{Operation: ops.Operation{ID: "op1", PlanID: id, State: state}, Events: []ops.Event{{Sequence: seq}}, NextCursor: seq}), nil
	case "diagnose":
		if err := agentExampleLog("diagnose"); err != nil {
			return result.Envelope{}, err
		}
		return (&diagnoseTransport{server: diagnosticServer()}).Call(context.Background(), transport.Target{}, req)
	}
	return result.Envelope{}, fmt.Errorf("unexpected operation %s", req.Op)
}
