package cli

import (
	"bytes"
	"context"
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
	}
}
