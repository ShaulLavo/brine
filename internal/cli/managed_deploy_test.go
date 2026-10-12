package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestManagedDeployNoInitWhileAdmissionUnavailable(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		t.Run(map[bool]string{false: "engine only", true: "frontend"}[frontend], func(t *testing.T) { checkManagedDeployNoInit(t, frontend) })
	}
}

func checkManagedDeployNoInit(t *testing.T, frontend bool) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	deps := testDependencies(t, &output, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	calls := []string{}
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls = append(calls, request.Op)
		if request.Op != "plan" {
			t.Fatalf("managed deploy attempted %s", request.Op)
		}
		var args dispatch.PlanArgs
		if json.Unmarshal(request.Args, &args) != nil || args.ManagedModule == nil || args.ManagedModule.Database != "main" || len(args.SchemaArtifacts) != 0 {
			t.Fatal("managed metadata not transported")
		}
		if _, err := dispatch.EncodeRequest(request); err != nil {
			t.Fatal(err)
		}
		return result.Success("brine host plan", dispatch.Planned{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Conflict, SchemaArtifacts: nil, Conflicts: []plan.Diagnostic{{Code: plan.ManagedEngineUnsupported, Field: "databases.spacetimedb"}}}), nil
	})
	err := Execute(deps, []string{"deploy", managedValidateSpec(t, frontend), "--target", "fixture", "--yes", "--json"})
	if result.Classify(err).Code() != result.Conflict || len(calls) != 1 || calls[0] != "plan" {
		t.Fatalf("managed no-init refusal changed: %v, calls %v", err, calls)
	}
}
