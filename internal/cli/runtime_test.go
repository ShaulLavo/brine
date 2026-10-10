package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
)

type runtimeRunner struct{ err error }

func (r runtimeRunner) Run(context.Context, string) error { return r.err }

type runtimeReconciler struct{}

func (runtimeReconciler) Reconcile(context.Context) (reconcile.Report, error) {
	return reconcile.Report{}, nil
}
func (runtimeReconciler) DryRun(context.Context) (reconcile.Report, error) {
	return reconcile.Report{DryRun: true}, nil
}

func TestRuntimeInitializationBoundary(t *testing.T) {
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, args := range [][]string{
		{"host", "run-op", "--help"}, {"help", "host", "reconcile"},
		{"host", "run-op"}, {"host", "run-op", "../bad"},
		{"host", "run-op", id, "extra"}, {"host", "reconcile", "extra"},
		{"host", "reconcile", "--unknown"}, {"host", "reconcile", "--json", "--jsonl"},
		{"host", "run-op", id}, {"host", "reconcile"}, {"host", "reconcile", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			opens, closes := 0, 0
			var output bytes.Buffer
			deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard, HostUID: func() int { return 1000 }}
			_ = ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(_ context.Context, preview bool) (RuntimeServices, error) {
				opens++
				if preview != strings.Contains(strings.Join(args, " "), "--dry-run") {
					t.Fatal("wrong opener mode")
				}
				return RuntimeServices{Runner: runtimeRunner{}, Reconciler: runtimeReconciler{}, Close: func() error { closes++; return nil }}, nil
			}})
			valid := strings.Join(args, " ") == "host run-op "+id || strings.Join(args, " ") == "host reconcile" || strings.Join(args, " ") == "host reconcile --dry-run"
			want := 0
			if valid {
				want = 1
			}
			if opens != want || closes != want {
				t.Fatalf("opens=%d closes=%d want=%d", opens, closes, want)
			}
		})
	}
}

func TestRuntimeResponseFinalization(t *testing.T) {
	for _, kind := range []string{"initialization", "close", "operation-and-close", "success", "dispatch-close", "dispatch-primary", "implicit-machine-close"} {
		t.Run(kind, func(t *testing.T) {
			var output bytes.Buffer
			closes := 0
			deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(`{"schema_version":1,"op":"ping","request_id":"fixture","args":{}}`), Stdout: &output, Stderr: io.Discard, HostUID: func() int { return 1000 }}
			primary := result.New(result.PolicyRefused, errors.New("private detail"))
			lifecycle := RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
				if kind == "initialization" {
					return RuntimeServices{}, primary
				}
				var opErr error
				if kind == "operation-and-close" {
					opErr = primary
				}
				return RuntimeServices{Runner: runtimeRunner{opErr}, Reconciler: runtimeReconciler{}, Close: func() error {
					closes++
					if kind != "success" {
						return errors.New("private close detail")
					}
					return nil
				}}, nil
			}}
			args := []string{"host", "run-op", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "--json"}
			if kind == "close" {
				args = []string{"host", "reconcile", "--json"}
			}
			if kind == "implicit-machine-close" {
				args = []string{"host", "reconcile"}
			}
			if strings.HasPrefix(kind, "dispatch") {
				args = []string{"host", "serve"}
				lifecycle.Close = func() error { closes++; return errors.New("private close detail") }
				if kind == "dispatch-primary" {
					deps.HostServerFactory = func(context.Context, string) (*dispatch.Server, error) { return nil, primary }
					deps.Stdin = strings.NewReader(`{"schema_version":1,"op":"inventory","request_id":"fixture","args":{}}`)
				}
			}
			err := ExecuteWithRuntime(deps, args, lifecycle)
			var envelope result.Envelope
			decoder := json.NewDecoder(&output)
			if e := decoder.Decode(&envelope); e != nil {
				t.Fatal(e)
			}
			if e := decoder.Decode(&result.Envelope{}); e != io.EOF {
				t.Fatalf("extra response: %v", e)
			}
			if kind == "dispatch-close" && envelope.Command != "brine host ping" {
				t.Fatalf("lost dispatch command: %s", envelope.Command)
			}
			if kind == "dispatch-primary" && envelope.Command != "brine host inventory" {
				t.Fatalf("lost dispatch command: %s", envelope.Command)
			}
			if envelope.OK != (result.ExitCode(err) == 0) {
				t.Fatalf("envelope/exit disagree: %+v %v", envelope, err)
			}
			if kind == "initialization" || kind == "operation-and-close" || kind == "dispatch-primary" {
				if err == nil || result.Classify(err).Code() != result.PolicyRefused || envelope.Error.Code != result.PolicyRefused {
					t.Fatalf("lost primary error: %v %+v", err, envelope)
				}
			} else if kind != "success" && (err == nil || result.Classify(err).Code() != result.InternalError) {
				t.Fatalf("lost close failure: %v", err)
			}
			if kind != "initialization" && closes != 1 {
				t.Fatalf("close count %d", closes)
			}
		})
	}
}

func TestRuntimeRootAndCancellationRefuseBeforeInitialization(t *testing.T) {
	for _, args := range [][]string{{"host", "run-op", "fixture", "--json"}, {"host", "reconcile", "--json"}} {
		for _, canceled := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			if canceled {
				cancel()
			}
			defer cancel()
			opens := 0
			uid := 0
			if canceled {
				uid = 1000
			}
			deps := Dependencies{Context: ctx, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, HostUID: func() int { return uid }}
			err := ExecuteWithRuntime(deps, args, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) { opens++; return RuntimeServices{}, nil }})
			if err == nil || opens != 0 {
				t.Fatalf("canceled=%v error=%v opens=%d", canceled, err, opens)
			}
		}
	}
}

func TestExplicitDetachedRunnerWinsOverMutationRuntime(t *testing.T) {
	for _, code := range []result.Code{result.DependencyMissing, result.PolicyRefused} {
		t.Run(string(code), func(t *testing.T) {
			var output bytes.Buffer
			calls, opens := 0, 0
			id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
			deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &output, Stderr: io.Discard, HostUID: func() int { return 1000 }, HostOperationRunner: opRunFunc(func(_ context.Context, got string) error {
				calls++
				if got != id {
					t.Fatal("wrong detached operation")
				}
				return nil
			})}
			err := ExecuteWithRuntime(deps, []string{"host", "run-op", id, "--json"}, RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
				opens++
				return RuntimeServices{}, result.New(code, nil)
			}})
			if err != nil || calls != 1 || opens != 0 {
				t.Fatalf("detached runner replaced: calls=%d mutation opens=%d error=%v", calls, opens, err)
			}
		})
	}
}
