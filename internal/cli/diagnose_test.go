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

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type diagnoseInventory struct{}

func (diagnoseInventory) Collect(context.Context) (target.Snapshot, error) {
	return target.Snapshot{OS: target.OS{ID: "debian", Version: "13"}, Arch: "arm64", FreeDiskBytes: target.Known(uint64(5)), Versions: target.Versions{Systemd: target.Known("257"), Podman: target.Known("5.4"), Caddy: target.Known("2.6"), Passt: target.Observation[string]{Status: target.Unknown}, Litestream: target.Observation[string]{Status: target.Absent}}, Runner: target.Runner{User: target.Known("brine"), Linger: target.Known(true)}, Apps: target.Known([]target.App{{Name: "demo", QuadletUnits: target.Known([]target.Unit{{Name: "demo.container", Hash: "sha256:" + strings.Repeat("a", 64)}})}}), LiveCaddyFiles: target.Known([]target.LiveCaddyFile{}), CaddyConfig: target.Known(target.CaddyConfigSet{Generation: 2, Files: []target.CaddyFile{}})}, nil
}

type diagnoseStore struct{}

func (diagnoseStore) AppNames(context.Context) ([]string, error) { return []string{"demo"}, nil }
func (diagnoseStore) RecentOperations(context.Context, string, int) ([]ops.OperationRecord, error) {
	return []ops.OperationRecord{{Operation: ops.Operation{ID: "op-fixture", State: ops.Failed, UpdatedAt: time.Unix(0, 0).UTC()}, FailureCode: "stale_plan"}}, nil
}
func (diagnoseStore) CurrentRelease(context.Context, string) (ops.Release, error) {
	return ops.Release{}, store.ErrNotFound
}
func (diagnoseStore) PreviousRelease(context.Context, string) (ops.Release, error) {
	return ops.Release{}, store.ErrNotFound
}
func (diagnoseStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{}, policy.Desired{}, store.ErrNotFound
}

type diagnoseLogs struct{}

func (diagnoseLogs) Read(context.Context, logs.Request) ([]logs.Line, error) {
	return []logs.Line{{Timestamp: "2026-10-09T00:00:00Z", Priority: 3, Message: "password=planted-diagnosis-secret"}}, nil
}

type diagnoseRuntime struct{}

func (diagnoseRuntime) RunStdout(_ context.Context, path string, args ...string) (string, error) {
	if path == "systemctl" {
		return "ActiveState=failed\nSubState=failed\nNRestarts=3\n", nil
	}
	return `{"name":"systemd-demo","running":false,"unit":"demo.service"}`, nil
}

type diagnoseTransport struct {
	server   *dispatch.Server
	requests []dispatch.Request
}

func (f *diagnoseTransport) Call(ctx context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
	f.requests = append(f.requests, request)
	raw, e := dispatch.EncodeRequest(request)
	if e != nil {
		return result.Envelope{}, e
	}
	response, e := f.server.Handle(ctx, bytes.NewReader(raw))
	if e != nil {
		return result.Envelope{}, e
	}
	raw, e = json.Marshal(response)
	if e != nil {
		return result.Envelope{}, e
	}
	return dispatch.DecodeResponse(raw, "diagnose")
}
func diagnosticServer() *dispatch.Server {
	s := dispatch.NewServer("test", diagnoseInventory{})
	s.Diagnose = diagnose.Reader{Inventory: diagnoseInventory{}, Store: diagnoseStore{}, Logs: diagnoseLogs{}, Runner: diagnoseRuntime{}, MinimumFreeDiskBytes: func(context.Context) (uint64, error) { return 10, nil }}
	return s
}
func TestDiagnoseCommandEndToEndGoldens(t *testing.T) {
	for _, mode := range []string{"human", "json", "jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out, errout bytes.Buffer
			client := &diagnoseTransport{server: diagnosticServer()}
			args := []string{"diagnose", "demo", "--target", "fixture", "--config-dir", targetConfig(t)}
			if mode != "human" {
				args = append(args, "--"+mode)
			}
			err := Execute(Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errout, OperationClient: client}, args)
			if err != nil {
				t.Fatal(err)
			}
			if len(client.requests) != 1 || client.requests[0].Op != "diagnose" {
				t.Fatal(client.requests)
			}
			if strings.Contains(out.String(), "planted-diagnosis-secret") || strings.Contains(errout.String(), "planted-diagnosis-secret") {
				t.Fatal("secret reached client")
			}
			path := filepath.Join("testdata", "diagnose."+mode)
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
				t.Fatalf("golden mismatch %s\n%s", path, out.String())
			}
			if mode != "human" {
				var envelope result.Envelope
				if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK {
					t.Fatal(out.String())
				}
			}
		})
	}
}
func TestDiagnoseHostAndValidation(t *testing.T) {
	class, ok := dispatch.ClassOf("diagnose")
	if !ok || class != dispatch.ReadOnly {
		t.Fatal("diagnose must be read-only")
	}
	for _, app := range []string{"", "demo"} {
		request := dispatch.Request{SchemaVersion: 1, Op: "diagnose", RequestID: "fixture", Args: json.RawMessage(`{"app":"` + app + `"}`)}
		raw, e := dispatch.EncodeRequest(request)
		if e != nil {
			t.Fatal(e)
		}
		var out, errout bytes.Buffer
		err := Execute(Dependencies{Context: context.Background(), Stdin: bytes.NewReader(raw), Stdout: &out, Stderr: &errout, HostUID: func() int { return 1000 }, HostDiagnose: diagnosticServer().Diagnose}, []string{"host", "serve"})
		if err != nil {
			t.Fatal(err)
		}
		response, e := dispatch.DecodeResponse(out.Bytes(), "diagnose")
		if e != nil || !response.OK {
			t.Fatalf("%s %v", out.String(), e)
		}
	}
	for _, args := range []string{`{"app":"demo;id"}`, `{"app":null}`, `{"app":"demo","unit":"other.service"}`, `{}`, `{"app":"demo","app":"demo"}`} {
		request := `{"schema_version":1,"op":"diagnose","request_id":"fixture","args":` + args + `}`
		if _, e := dispatch.DecodeRequest([]byte(request)); e == nil {
			t.Fatal("accepted invalid diagnostic request")
		}
	}
	for _, args := range [][]string{{"diagnose", "demo", "--json"}, {"diagnose", "demo;id", "--target", "fixture", "--json"}, {"diagnose", "a", "b", "--target", "fixture", "--json"}} {
		var out, errout bytes.Buffer
		client := &diagnoseTransport{server: diagnosticServer()}
		err := Execute(Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errout, OperationClient: client}, args)
		if err == nil || len(client.requests) != 0 {
			t.Fatal("accepted invalid CLI request")
		}
	}
}

func TestEveryDiagnosisSuggestionIsRegistered(t *testing.T) {
	failed := diagnose.App{Name: "demo", Unit: diagnose.Known(diagnose.Unit{ActiveState: "failed", Restarts: 3}), ContainerRunning: diagnose.Known(false), Health: diagnose.Known(false), RoutePresent: diagnose.Known(false), Drift: diagnose.Known([]string{"units"}), Operations: diagnose.Known([]diagnose.RecentOperation{{ID: "op-fixture", State: ops.Failed, FailureCode: "stale_plan"}})}
	recovering := diagnose.App{Name: "demo-recovery", Operations: diagnose.Known([]diagnose.RecentOperation{{ID: "op-recovery", State: ops.RecoveryRequired}})}
	report := diagnose.Report{Host: diagnose.Host{FreeDiskBytes: diagnose.Known(uint64(1)), MinimumFreeDiskBytes: diagnose.Known(uint64(2)), Linger: diagnose.Known(false)}, Apps: []diagnose.App{failed, recovering}}
	findings := diagnose.Findings(report)
	if len(findings) != 11 {
		t.Fatalf("must exercise every finding rule, got %d", len(findings))
	}
	root := NewRootCommand(Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	for _, finding := range findings {
		for _, suggestion := range finding.NextOperations {
			words := strings.Fields(suggestion)
			if len(words) < 2 || words[0] != "brine" {
				t.Fatalf("invalid suggestion %q", suggestion)
			}
			cmd, _, err := root.Find(words[1:])
			if err != nil || cmd == root || cmd.Name() != words[1] {
				t.Fatalf("%s suggests unregistered command %q: %v", finding.Code, suggestion, err)
			}
		}
	}
}
