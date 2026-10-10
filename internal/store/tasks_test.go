//go:build linux

package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestDetachedTaskReferenceJournal(t *testing.T) {
	state := openTest(t)
	for _, kind := range []ops.Kind{"restore_test", "credential_activation", "data_init_apply"} {
		intent := ops.Intent{Kind: kind, App: "example", SecretRef: "immutable-reference"}
		operation, existing, err := state.CreateOperation(context.Background(), intent, "operator", string(kind))
		if err != nil || existing || operation.Kind != kind || operation.SecretRef != intent.SecretRef {
			t.Fatalf("detached reference rejected: %s %v", kind, err)
		}
	}
}

func TestTaskCompletionAtomicAndBounded(t *testing.T) {
	ctx := context.Background()
	state := openTest(t)
	intent := ops.Intent{Kind: ops.RestoreTest, App: "example", SecretRef: "input1"}
	operation, _, err := state.CreateOperation(ctx, intent, "operator", "task1")
	if err != nil {
		t.Fatal(err)
	}
	changed := intent
	changed.SecretRef = "other-input"
	if _, _, err := state.CreateOperation(ctx, changed, "operator", "task1"); err == nil {
		t.Fatal("idempotency key rebound to another reference")
	}
	changed = intent
	changed.App = "different"
	if _, _, err := state.CreateOperation(ctx, changed, "operator", "task1"); err == nil {
		t.Fatal("idempotency key rebound to another app")
	}
	if err := state.TransitionOperation(ctx, operation.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := state.CompleteTask(ctx, operation.ID, ops.Succeeded, ops.TaskOutcome{Receipt: []byte(`{"private":"PLANTED"}`)}); err == nil {
		t.Fatal("untyped receipt accepted")
	}
	current, err := state.GetOperation(ctx, operation.ID)
	if err != nil || current.State != ops.Preflight {
		t.Fatal("invalid receipt advanced operation", err)
	}
	outcome, err := state.ReadTaskOutcome(ctx, operation.ID)
	if err != nil || outcome != nil {
		t.Fatal("partial outcome persisted", err)
	}
	safe := ops.TaskOutcome{Error: result.PolicyRefused}
	if err := state.CompleteTask(ctx, operation.ID, ops.Failed, safe); err != nil {
		t.Fatal(err)
	}
	outcome, err = state.ReadTaskOutcome(ctx, operation.ID)
	if err != nil || outcome == nil || outcome.Error != result.PolicyRefused {
		t.Fatal("error outcome lost", err)
	}
	if err := state.CompleteTask(ctx, operation.ID, ops.Failed, safe); err == nil {
		t.Fatal("second completion admitted")
	}
	events, err := state.EventsAfter(ctx, operation.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].State != ops.Failed {
		t.Fatal("terminal state/event were not atomic", err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE operation_outcomes SET canonical='{}' WHERE operation_id=?", operation.ID); err == nil {
		t.Fatal("outcome mutable")
	}
}
