package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type appOperations struct {
	report       apps.Report
	planned      apps.RollbackPlan
	app, release string
}

func (a *appOperations) Status(_ context.Context, app string) (apps.Report, error) {
	a.app = app
	if app == "missing" {
		return apps.Report{}, result.New(result.AppNotFound, nil)
	}
	return a.report, nil
}
func (a *appOperations) Rollback(_ context.Context, app, release string) (apps.RollbackPlan, error) {
	a.app, a.release = app, release
	return a.planned, nil
}
func appResults() *appOperations {
	digest := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	current := apps.Release{ID: "current", PlanID: digest("a"), ImageDigest: digest("b"), Port: 20000, Domains: []spec.Domain{"hello.example.com"}}
	previous := current
	previous.ID = "previous"
	previous.PlanID = digest("c")
	previous.ImageDigest = digest("d")
	before := plan.Image{Digest: current.ImageDigest, Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Observation[string]{Status: target.Unknown}}
	after := before
	after.Digest = previous.ImageDigest
	return &appOperations{report: apps.Report{Apps: []apps.Status{{App: "hello", Current: current, Previous: &previous, LastOperation: &ops.Operation{Kind: ops.Deploy, ID: "op-failed", PlanID: digest("e"), State: ops.Failed, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)}, Health: apps.Health{UnitActive: target.Known(true), Direct: "healthy"}, Drift: apps.Drift{State: "in_sync", Fields: []string{}}}}}, planned: apps.RollbackPlan{PlanID: digest("f"), ReleaseID: "previous", Compatibility: "stateless_compatible", Kind: plan.Update, Diff: &plan.ConfigurationDiff{Image: &plan.ValueChange[plan.Image]{From: &before, To: &after}, Secrets: []plan.SecretChange{}}, Conflicts: []plan.Diagnostic{}}}
}
func TestAppMachineGoldens(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		empty    bool
		wantCode int
	}{
		{"app-status.json", []string{"status", "hello", "--json"}, false, 0},
		{"app-status.jsonl", []string{"status", "--jsonl"}, false, 0},
		{"app-empty.json", []string{"status", "--json"}, true, 0},
		{"app-empty.jsonl", []string{"status", "--jsonl"}, true, 0},
		{"app-missing.json", []string{"status", "missing", "--json"}, false, 2},
		{"rollback.json", []string{"rollback", "hello", "--json"}, false, 0},
		{"rollback.jsonl", []string{"rollback", "hello", "--release", "previous", "--jsonl"}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := appResults()
			if tc.empty {
				service.report.Apps = []apps.Status{}
			}
			server := dispatch.NewServer("test", nil).WithJobs(nil, func(_ context.Context, class dispatch.Class) error {
				if tc.args[0] == "rollback" && class != dispatch.Mutating {
					t.Fatal(class)
				}
				return nil
			})
			server.Apps = service
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			calls := 0
			deps.OperationClient = callFunc(func(ctx context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
				calls++
				raw, e := dispatch.EncodeRequest(req)
				if e != nil {
					t.Fatal(e)
				}
				env, e := server.Handle(ctx, bytes.NewReader(raw))
				encoded, _ := json.Marshal(env)
				decoded, de := dispatch.DecodeResponse(encoded, req.Op)
				if de != nil {
					t.Fatal(de, string(encoded))
				}
				return decoded, e
			})
			args := append(append([]string{}, tc.args...), "--target", "fixture")
			err := Execute(deps, args)
			if result.ExitCode(err) != tc.wantCode || calls != 1 {
				t.Fatal(err, calls, out.String())
			}
			path := filepath.Join("testdata", tc.name)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if e := os.WriteFile(path, out.Bytes(), 0600); e != nil {
					t.Fatal(e)
				}
			}
			want, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(want, out.Bytes()) {
				t.Fatalf("want %s\ngot %s", want, out.String())
			}
			if tc.args[0] == "rollback" && service.app != "hello" {
				t.Fatal(service.app)
			}
			if strings.Contains(out.String(), "environment\":{") {
				t.Fatal("literal settings leaked")
			}
		})
	}
}
func TestAppHumanOutput(t *testing.T) {
	for _, command := range []string{"status", "rollback"} {
		t.Run(command, func(t *testing.T) {
			service := appResults()
			var out, stderr bytes.Buffer
			deps := testDependencies(t, &out, &stderr)
			deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
			deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
				if command == "status" {
					return result.Success("brine host status", service.report), nil
				}
				return result.Success("brine host rollback", service.planned), nil
			})
			if e := Execute(deps, []string{command, "hello", "--target", "fixture"}); e != nil {
				t.Fatal(e)
			}
			expected := []string{"Current:", "Previous known-good:", "op-failed (failed)", "direct health: healthy", "in_sync"}
			if command == "rollback" {
				expected = []string{"Rollback plan", "old -> new", "brine apply", "never rewinds data"}
			}
			for _, text := range expected {
				if !strings.Contains(out.String(), text) {
					t.Fatal(out.String())
				}
			}
		})
	}
}
func TestAppArgumentRefusalsBeforeIO(t *testing.T) {
	for _, args := range [][]string{
		{"status"}, {"status", "../bad", "--target", "fixture"}, {"status", "hello", "--operation", "op1", "--target", "fixture"}, {"status", "--after-cursor", "0", "--target", "fixture"}, {"rollback", "hello"}, {"rollback", "hello", "--release", "", "--target", "fixture"}, {"rollback", "hello", "--release", "../bad", "--target", "fixture"},
	} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
			t.Fatal("invalid args performed IO")
			return transport.Target{}, nil
		}
		if e := Execute(deps, append(args, "--json")); result.ExitCode(e) != 2 {
			t.Fatal(args, e)
		}
	}
}
