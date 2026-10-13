package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestWaitTaskNoWaitDoesNotPoll(t *testing.T) {
	accepted := jobs.Accepted{Status: "accepted", OperationID: "op1"}
	value, err := (operationFlags{}).waitTask(context.Background(), Dependencies{}, accepted, ops.RestoreTest, true)
	if err != nil || value != accepted {
		t.Fatal("no-wait lost acceptance", err)
	}
}

func TestWaitTaskFixedTerminalErrorAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		var out, stderr bytes.Buffer
		deps := testDependencies(t, &out, &stderr)
		deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
		requests := 0
		deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
			requests++
			if request.Op != "operation" {
				t.Fatal("poll used wrong operation")
			}
			op := ops.Operation{ID: "op1", Kind: ops.RestoreTest, App: "example", SecretRef: "input1", State: ops.Failed}
			status := jobs.Status{Operation: op, Events: []ops.Event{}, Outcome: &ops.TaskOutcome{Error: result.PolicyRefused}}
			if cancel {
				status.Operation.State = ops.Preflight
				status.Outcome = nil
			}
			return result.Success("brine host operation", status), nil
		})
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, err := (operationFlags{target: "fixture"}).waitTask(ctx, deps, jobs.Accepted{Status: "accepted", OperationID: "op1"}, ops.RestoreTest, false)
		stop()
		want := result.PolicyRefused
		if cancel {
			want = result.Interrupted
		}
		if result.Classify(err).Code() != want || requests != 1 {
			t.Fatalf("poll result: %v requests=%d", err, requests)
		}
		var acceptedFailure *acceptedTaskFailure
		if !errors.As(err, &acceptedFailure) || acceptedFailure.accepted.OperationID != "op1" {
			t.Fatal("terminal/canceled wait lost accepted locator")
		}
	}
}

func TestAcceptedTaskFailureRetainsOnlyValidatedLocatorAndTypedCause(t *testing.T) {
	accepted := jobs.Accepted{Status: "accepted", OperationID: "accepted-op"}
	cause := result.New(result.PolicyRefused, errors.New("PRIVATE_DIAGNOSTIC"))
	_, err := waitAcceptedTask(context.Background(), accepted, ops.NativeBackupResolve, false, func(context.Context, dispatch.OperationArgs) (jobs.Status, error) { return jobs.Status{}, cause })
	var failure *acceptedTaskFailure
	if !errors.As(err, &failure) || failure.accepted != accepted || !errors.Is(err, cause) || result.Classify(err).Code() != result.PolicyRefused || result.ExitCode(err) != result.ExitCode(cause) || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC") {
		t.Fatal("acceptance lost typed cause or leaked diagnostics", err)
	}
	_, err = waitAcceptedTask(context.Background(), jobs.Accepted{Status: "accepted", OperationID: "../PRIVATE_ID"}, ops.NativeBackupResolve, false, nil)
	if errors.As(err, &failure) {
		t.Fatal("invalid acceptance exposed locator")
	}
	_, err = waitAcceptedTask(context.Background(), accepted, ops.NativeBackupResolve, false, func(context.Context, dispatch.OperationArgs) (jobs.Status, error) {
		return jobs.Status{Operation: ops.Operation{ID: "foreign-status-op", Kind: ops.NativeBackupResolve, State: ops.RecoveryRequired}}, nil
	})
	if !errors.As(err, &failure) || failure.accepted.OperationID != accepted.OperationID || result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatal("crossed status changed original accepted locator")
	}
}

func TestTaskWaitFailureEnvelopeKeepsAcceptedOperationID(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	submitted, polls := 0, 0
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, request dispatch.Request) (result.Envelope, error) {
		if request.Op != "operation" {
			submitted++
			return result.Success("brine host native_backup_resolve", jobs.Accepted{Status: "accepted", OperationID: "accepted-resolver"}), nil
		}
		polls++
		return result.Success("brine host operation", jobs.Status{Operation: ops.Operation{ID: "accepted-resolver", Kind: ops.NativeBackupResolve, State: ops.RecoveryRequired}, Outcome: &ops.TaskOutcome{Error: result.InternalError}}), nil
	})
	err := Execute(deps, []string{"backup", "recovery", "resolve", strings.Repeat("b", 64), "--target", "fixture", "--idempotency-key", strings.Repeat("a", 32), "--json"})
	var envelope result.Envelope
	if json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != result.InternalError {
		t.Fatal("terminal failure changed success/code")
	}
	raw, _ := json.Marshal(envelope.Data)
	var accepted jobs.Accepted
	if json.Unmarshal(raw, &accepted) != nil || accepted.OperationID != "accepted-resolver" || accepted.Status != "accepted" || result.Classify(err).Code() != result.InternalError || submitted != 1 || polls != 1 || !strings.Contains(stderr.String(), "operation_id=accepted-resolver") {
		t.Fatal("failed wait lost locator or repeated work", err)
	}
}
