package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestTrustedMarkerGateForEveryOperation(t *testing.T) {
	for _, marker := range []string{"", "wrong", "deploy"} {
		for _, op := range []string{"ping", "inventory", "plan", "apply", "operation", "status", "rollback", "logs", "diagnose", "reconcile"} {
			t.Run(marker+"/"+op, func(t *testing.T) {
				state := filepath.Join(t.TempDir(), "state")
				opens, inventories := 0, 0
				f := newServerFactory("fixture", marker, func(_ context.Context, captured string) (*Runtime, error) {
					opens++
					if captured != "deploy" {
						t.Fatal("untrusted marker reached runtime")
					}
					if err := os.Mkdir(state, 0700); err != nil {
						t.Fatal(err)
					}
					return &Runtime{close: func() error { return nil }}, nil
				}, func(context.Context) (dispatch.Inventory, error) { inventories++; return nil, nil })
				_, err := f.Build(context.Background(), op)
				allowed := op == "ping" || marker == "deploy"
				if allowed && err != nil || !allowed && (err == nil || result.Classify(err).Code() != result.DispatchOperationRefused) {
					t.Fatalf("allowed=%v error=%v", allowed, err)
				}
				if !allowed || op == "ping" || op == "inventory" {
					if opens != 0 {
						t.Fatal("opened store for refused or no-store operation")
					}
					if _, err := os.Stat(state); !os.IsNotExist(err) {
						t.Fatal("created deployment state")
					}
				} else if opens != 1 {
					t.Fatal("did not configure runtime")
				}
				if inventories != 0 && (op != "inventory" || marker != "deploy") {
					t.Fatal("untrusted inventory initialization")
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestFactoryRequiresRuntimeAuthorizationForMutations(t *testing.T) {
	spec, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"plan":      dispatch.PlanArgs{Spec: string(spec)},
		"apply":     dispatch.ApplyArgs{PlanID: "sha256:" + strings.Repeat("a", 64), IdempotencyKey: "fixture-key"},
		"reconcile": dispatch.ReconcileArgs{DryRun: false},
		"rollback":  dispatch.RollbackArgs{App: "hello", ReleaseID: ""},
	}
	for op, arg := range args {
		for _, deny := range []bool{false, true} {
			var authorize dispatch.Authorization
			if deny {
				authorize = func(context.Context, dispatch.Class) error { return result.New(result.PolicyRefused, nil) }
			}
			f := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
				return &Runtime{Authorize: authorize, close: func() error { return nil }}, nil
			}, nil)
			server, err := f.Build(context.Background(), op)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(arg)
			if err != nil {
				t.Fatal(err)
			}
			request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: op, RequestID: "fixture", Args: raw})
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Handle(context.Background(), bytes.NewReader(request))
			code := result.DispatchOperationRefused
			if deny {
				code = result.PolicyRefused
			}
			if err == nil || response.OK || result.Classify(err).Code() != code {
				t.Fatalf("op=%s deny=%v error=%v", op, deny, err)
			}
		}
	}
}

func TestFactoryWiresRealReconciler(t *testing.T) {
	r := newDeployRig(t)
	factory := newServerFactory("fixture", "deploy", func(context.Context, string) (*Runtime, error) {
		return &Runtime{Reconciler: r.server.Reconciler, Authorize: r.service.Authorize, close: func() error { return nil }}, nil
	}, nil)
	defer factory.Close()
	server, err := factory.Build(context.Background(), "reconcile")
	if err != nil || server.Reconciler == nil {
		t.Fatalf("production recovery missing: %v", err)
	}
}
