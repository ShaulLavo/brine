package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestLifecycleHelpersKeepCommandOutput(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.RestartApp, plan.StopApp, plan.StartApp} {
		t.Run(string(action), func(t *testing.T) { checkLifecycleCommandModes(t, action) })
	}
}

func checkLifecycleCommandModes(t *testing.T, action plan.ChangeKind) {
	t.Helper()
	for _, mode := range []string{"", "--json", "--jsonl"} {
		t.Run(mode, func(t *testing.T) { checkLifecycleCommandOutput(t, action, mode) })
	}
}

func checkLifecycleCommandOutput(t *testing.T, action plan.ChangeKind, mode string) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	p := apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Lifecycle: action, Conflicts: []plan.Diagnostic{}}
	calls := 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		var args dispatch.LifecycleArgs
		if request.Op != "lifecycle" || json.Unmarshal(request.Args, &args) != nil || args.App != "hello" || args.Action != action {
			t.Fatalf("wrong lifecycle request: %+v", request)
		}
		return result.Success("fixture", p), nil
	})
	verb := strings.TrimSuffix(string(action), "_app")
	args := []string{verb, "hello", "--target", "fixture"}
	if mode != "" {
		args = append(args, mode)
	}
	if err := Execute(deps, args); err != nil || calls != 1 {
		t.Fatalf("lifecycle = %v, calls = %d", err, calls)
	}
	want := "Plan " + p.PlanID + " (update). No app changes have been applied.\nAction: " + string(action) + ". Releases and app data stay unchanged.\nApply with brine apply " + p.PlanID + " --target fixture.\n"
	if mode != "" {
		raw, err := json.Marshal(result.Success("brine "+verb, p))
		if err != nil {
			t.Fatal(err)
		}
		want = string(raw) + "\n"
	}
	if out.String() != want || stderr.Len() != 0 {
		t.Fatalf("output changed:\n%s\nwant:\n%s\nstderr: %s", out.String(), want, stderr.String())
	}
}

func TestSharedHelpersRefuseMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(context.Context, Dependencies, operationFlags) error
	}{
		{"apps", func(ctx context.Context, deps Dependencies, f operationFlags) error {
			_, err := readAppReport(ctx, deps, f, "")
			return err
		}},
		{"operation", func(ctx context.Context, deps Dependencies, f operationFlags) error {
			_, err := readOperationPage(ctx, deps, f, "op1", 0)
			return err
		}},
		{"apply", func(ctx context.Context, deps Dependencies, f operationFlags) error {
			_, err := acceptPlan(ctx, deps, f, "sha256:"+strings.Repeat("a", 64), "key")
			return err
		}},
		{"lifecycle", func(ctx context.Context, deps Dependencies, f operationFlags) error {
			_, err := planLifecycle(ctx, deps, f, "hello", plan.RestartApp)
			return err
		}},
		{"rollback", func(ctx context.Context, deps Dependencies, f operationFlags) error {
			_, err := planRollback(ctx, deps, f, "hello", "")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { checkMalformedSharedResponse(t, tc.read) })
	}
}

func checkMalformedSharedResponse(t *testing.T, read func(context.Context, Dependencies, operationFlags) error) {
	t.Helper()
	deps := Dependencies{LoadOperationTarget: func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }}
	deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
		return result.Success("fixture", "wrong response type"), nil
	})
	if err := read(t.Context(), deps, operationFlags{target: "fixture"}); result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatalf("malformed response = %v", err)
	}
}

func TestWaitAcceptedTaskKeepsKindCheck(t *testing.T) {
	_, err := waitAcceptedTask(t.Context(), jobs.Accepted{Status: "accepted", OperationID: "op1"}, ops.RestoreTest, false, func(context.Context, dispatch.OperationArgs) (jobs.Status, error) {
		return jobs.Status{Operation: ops.Operation{ID: "op1", Kind: ops.NativeRestoreTest, State: ops.RecoveryRequired}}, nil
	})
	if result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatalf("crossed operation kind = %v", err)
	}
}
