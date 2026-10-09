package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestResolveCLIUsesClosedAcceptanceProtocol(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	deps := Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: &out, Stderr: io.Discard}
	deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		calls++
		var args dispatch.ResolveArgs
		if request.Op != "resolve" || json.Unmarshal(request.Args, &args) != nil || args.OperationID != "source-operation" || args.IdempotencyKey != "stable-key" {
			t.Fatalf("request %+v", request)
		}
		if _, err := dispatch.EncodeRequest(request); err != nil {
			t.Fatal(err)
		}
		return result.Success("brine resolve", jobs.Accepted{Status: "accepted", OperationID: "resolution-job"}), nil
	})
	if err := Execute(deps, []string{"resolve", "source-operation", "--target", "fixture", "--idempotency-key", "stable-key", "--json"}); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	if !strings.Contains(out.String(), "resolution-job") {
		t.Fatal(out.String())
	}
	calls = 0
	if err := Execute(deps, []string{"resolve", "../untrusted", "--target", "fixture"}); err == nil || calls != 0 {
		t.Fatal("invalid source reached transport", err, calls)
	}
}

func TestRecoveryStatusSuggestsSupportedCommand(t *testing.T) {
	for _, kind := range []ops.Kind{ops.Deploy, ops.SecretSet, ops.Resolve, ops.Reconcile} {
		t.Run(string(kind), func(t *testing.T) {
			var out bytes.Buffer
			deps := Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: &out, Stderr: io.Discard}
			deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }

			events := []ops.Event{}
			operation := ops.Operation{ID: "receipt", Kind: kind, State: ops.RecoveryRequired, App: "hello", PlanID: "fixture-plan"}
			if kind == ops.SecretSet {
				operation.PlanID = ""
				operation.SecretRef = "token"
				payload, _ := json.Marshal(ops.SecretVersionPayload{Name: "brine.hello.token.v1", Outcome: "unknown"})
				events = append(events, ops.Event{Sequence: 1, Kind: "secret_version", Payload: payload})
			} else if kind != ops.Reconcile {
				for i, step := range []string{"preflight", "withdraw_route", "stop_unit"} {
					payload, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: "completed"})
					events = append(events, ops.Event{Sequence: uint64(i + 1), Kind: "step", Payload: payload})
				}
			}
			deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
				return result.Success("brine host operation", jobs.Status{Operation: operation, Events: events, NextCursor: uint64(len(events))}), nil
			})

			if err := Execute(deps, []string{"status", "--operation", "receipt", "--target", "fixture"}); err != nil {
				t.Fatal(err)
			}
			if kind == ops.Reconcile {
				if strings.Contains(out.String(), "brine resolve") || !strings.Contains(out.String(), "brine reconcile --dry-run") {
					t.Fatal(out.String())
				}
			} else if !strings.Contains(out.String(), "brine resolve receipt --target fixture") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestStatusDoesNotOfferResolveWithoutSupportedPrefix(t *testing.T) {
	for _, step := range []string{"", "rollback_quiesce", "stop_unit"} {
		t.Run(step, func(t *testing.T) {
			var out bytes.Buffer
			events := []ops.Event{}
			if step != "" {
				payload, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: "unknown", Code: "interrupted"})
				events = append(events, ops.Event{Sequence: 1, Kind: "step", Payload: payload})
			}
			deps := Dependencies{Context: context.Background(), Stdin: bytes.NewReader(nil), Stdout: &out, Stderr: io.Discard}
			deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
			deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
				return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: "receipt", Kind: ops.Deploy, State: ops.RecoveryRequired}, Events: events, NextCursor: uint64(len(events))}), nil
			})
			if err := Execute(deps, []string{"status", "--operation", "receipt", "--target", "fixture"}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "brine resolve") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestResolutionGuidanceRecognizesProvenNoEffectRefusal(t *testing.T) {
	operation := ops.Operation{ID: "receipt", Kind: ops.Resolve, State: ops.RecoveryRequired, PlanID: "fixture-plan"}
	var events []ops.Event
	appendStep := func(step, outcome, code string) {
		payload, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: outcome, Code: code})
		events = append(events, ops.Event{Kind: "step", Payload: payload})
	}
	for _, step := range []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit"} {
		appendStep(step, "completed", "")
	}
	appendStep("check_direct", "intent", "")
	appendStep("check_direct", "failed", "health_failed")
	appendStep("rollback_quiesce", "intent", "")
	appendStep("rollback_quiesce", "failed", "rollback_failed")
	if resolutionPrefixSupported(operation, events) {
		t.Fatal("ambiguous failure received resolve advice")
	}
	events = events[:len(events)-1]
	appendStep("rollback_quiesce", "failed", "effect_refused")
	if !resolutionPrefixSupported(operation, events) {
		t.Fatal("proven no-effect refusal suppressed inspection advice")
	}
}
