package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestHostReconcileRuntimeSelectionAndMissingDependency(t *testing.T) {
	if !HostRuntimeRequested([]string{"host", "reconcile", "--json"}) {
		t.Fatal("boot command missed runtime setup")
	}
	err := Execute(Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: io.Discard, Stderr: io.Discard, HostUID: func() int { return 1001 }}, []string{"host", "reconcile", "--json"})
	if result.Classify(err).Code() != result.DependencyMissing {
		t.Fatalf("boot entry point error %v", err)
	}
}

type fakeReconciler struct{ calls int }

func (f *fakeReconciler) Reconcile(context.Context) (reconcile.Report, error) {
	f.calls++
	return reconcile.Report{Outcomes: []reconcile.Outcome{}}, nil
}
func (f *fakeReconciler) DryRun(context.Context) (reconcile.Report, error) {
	f.calls++
	return reconcile.Report{DryRun: true, Outcomes: []reconcile.Outcome{}}, nil
}

func TestHostReconcileRootRefusalAndPreview(t *testing.T) {
	for _, uid := range []int{0, 1001} {
		engine := &fakeReconciler{}
		var output bytes.Buffer
		err := Execute(Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: &output, Stderr: io.Discard, HostUID: func() int { return uid }, HostReconciler: engine}, []string{"host", "reconcile", "--json", "--dry-run"})
		if uid == 0 {
			if result.Classify(err).Code() != result.DispatchRootRefused || engine.calls != 0 {
				t.Fatal("root reached recovery")
			}
			continue
		}
		if err != nil || engine.calls != 1 {
			t.Fatalf("reconcile error %v calls %d", err, engine.calls)
		}
		envelope, err := dispatch.DecodeResponse(output.Bytes(), "reconcile")
		if err != nil || !envelope.OK {
			t.Fatalf("preview response %v", err)
		}
		if !envelope.Data.(reconcile.Report).DryRun {
			t.Fatal("preview changed to mutation")
		}
	}
}

func TestRemoteReconcileCLI(t *testing.T) {
	for _, dry := range []bool{false, true} {
		var output bytes.Buffer
		deps := Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: &output, Stderr: io.Discard}
		deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
		calls := 0
		deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
			calls++
			if request.Op != "reconcile" {
				t.Fatal("wrong operation")
			}
			var args dispatch.ReconcileArgs
			if err := json.Unmarshal(request.Args, &args); err != nil || args.DryRun != dry {
				t.Fatalf("wrong preview arguments %s", request.Args)
			}
			if _, err := dispatch.EncodeRequest(request); err != nil {
				t.Fatal(err)
			}
			return result.Success("brine host reconcile", reconcile.Report{DryRun: dry, Outcomes: []reconcile.Outcome{}}), nil
		})
		args := []string{"reconcile", "--target", "fixture", "--json"}
		if dry {
			args = append(args, "--dry-run")
		}
		if err := Execute(deps, args); err != nil || calls != 1 {
			t.Fatalf("calls %d error %v", calls, err)
		}
		if !json.Valid(output.Bytes()) {
			t.Fatal("machine response invalid")
		}
	}
}
