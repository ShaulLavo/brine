package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

func shellArguments(t *testing.T, command string) []string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "set -- "+command+"; printf '%s\\000' \"$@\"").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

func TestFollowUpCommandsRetainConfiguration(t *testing.T) {
	planID := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name, marker, suffix string
		args                 []string
		data                 any
	}{
		{"config", "Apply with ", ".\n", []string{"config", "set", "hello", "TOKEN=PLANTED_PRIVATE_ENV"}, apps.ConfigPlan{PlanID: planID, Kind: plan.Update}},
		{"lifecycle", "Apply with ", ".\n", []string{"restart", "hello"}, apps.ConfigPlan{PlanID: planID, Kind: plan.Update, Lifecycle: plan.RestartApp}},
		{"rollback", "Apply with ", ". App rollback never rewinds data.\n", []string{"rollback", "hello"}, apps.RollbackPlan{PlanID: planID, Kind: plan.Update, ReleaseID: "previous", Compatibility: "stateless_compatible"}},
		{"apply", "Check with ", ".\n", []string{"apply", planID}, jobs.Accepted{Status: "accepted", OperationID: "receipt"}},
		{"resolve", "Check with ", ".\n", []string{"resolve", "source"}, jobs.Accepted{Status: "accepted", OperationID: "receipt"}},
		{"reconcile", "Poll with: ", "\n", []string{"reconcile"}, jobs.Accepted{Status: "accepted", OperationID: "receipt"}},
		{"recovery-reconcile", "preview remaining recovery with: ", "\n", []string{"status", "--operation", "receipt"}, jobs.Status{Operation: ops.Operation{ID: "receipt", Kind: ops.Reconcile, State: ops.RecoveryRequired}}},
		{"recovery-resolve", "Inspect supported recovery with: ", "\n", []string{"status", "--operation", "receipt"}, jobs.Status{Operation: ops.Operation{ID: "receipt", Kind: ops.SecretSet, State: ops.RecoveryRequired, App: "hello", SecretRef: "token"}, Events: []ops.Event{{Sequence: 1, Kind: "secret_version", Payload: json.RawMessage(`{"name":"brine.hello.token.v1","outcome":"unknown"}`)}}, NextCursor: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			defaultDir := filepath.Join(home, "brine", "targets")
			customDir := filepath.Join(t.TempDir(), "custom space ' $(printf UNQUOTED) ;")
			for _, dir := range []string{defaultDir, customDir} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				target := transport.Target{Name: "fixture", Destination: "runner@default-fixture", IdentityPath: "/fixture/key", PinnedHostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
				if dir == customDir {
					target.Destination = "runner@custom-fixture"
				}
				raw, err := json.Marshal(target)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			calls := 0
			deps.LoadOperationTarget = func(dir, name string) (transport.Target, error) {
				return loadOperationTarget(dir, name)
			}
			deps.OperationClient = callFunc(func(_ context.Context, target transport.Target, request dispatch.Request) (result.Envelope, error) {
				calls++
				if target.Destination != "runner@custom-fixture" {
					t.Errorf("follow-up selected %s", target.Destination)
				}
				if calls == 1 {
					return result.Success("fixture", test.data), nil
				}
				switch request.Op {
				case "apply", "resolve":
					return result.Success("fixture", jobs.Accepted{Status: "accepted", OperationID: "next-receipt"}), nil
				case "operation":
					return result.Success("fixture", jobs.Status{Operation: ops.Operation{ID: "receipt", State: ops.Succeeded}}), nil
				case "reconcile":
					return result.Envelope{}, result.New(result.Conflict, nil)
				default:
					t.Fatalf("unexpected follow-up %s", request.Op)
					return result.Envelope{}, nil
				}
			})
			args := append(append([]string{}, test.args...), "--target", "fixture", "--config-dir", customDir)
			if err := Execute(deps, args); err != nil {
				t.Fatal(err)
			}
			_, command, found := strings.Cut(out.String(), test.marker)
			if !found {
				t.Fatalf("no guidance in %s", out.String())
			}
			command = strings.TrimSuffix(command, test.suffix)
			followUp := shellArguments(t, command)
			if followUp[0] != "brine" {
				t.Fatalf("command=%v", followUp)
			}
			out.Reset()
			followUpErr := Execute(deps, followUp[1:])
			if test.name == "recovery-reconcile" {
				if followUpErr == nil || result.Classify(followUpErr).Code() != result.Conflict {
					t.Fatalf("reconcile follow-up err=%v", followUpErr)
				}
			} else if followUpErr != nil {
				t.Fatalf("copied command failed: %v", followUpErr)
			}
			if calls != 2 {
				t.Fatalf("copied command did not contact original target: %v; stderr=%s", followUp, stderr.String())
			}
		})
	}
}

func TestDiagnoseFollowUpsRetainConfiguration(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.OperationClient = &diagnoseTransport{server: diagnosticServer()}
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	configDir := filepath.Join(t.TempDir(), "space ' $(printf UNQUOTED)")
	if err := Execute(deps, []string{"diagnose", "demo", "--target", "fixture", "--config-dir", configDir}); err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, line := range strings.Split(out.String(), "\n") {
		command, ok := strings.CutPrefix(line, "  Next: ")
		if !ok || !strings.Contains(command, "--target") {
			continue
		}
		commands++
		args := shellArguments(t, command)
		found := false
		for i, arg := range args {
			if arg == "--config-dir" && i+1 < len(args) && args[i+1] == configDir {
				found = true
			}
		}
		if !found {
			t.Fatalf("diagnose follow-up lost config directory: %s", command)
		}
	}
	if commands == 0 {
		t.Fatal("no target-specific guidance")
	}
}
