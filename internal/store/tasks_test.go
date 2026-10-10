//go:build linux

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
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

func TestTaskOutcomeConcurrentCompletionSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writer := openTest(t)
	reader, err := OpenReadOnly(ctx, writer.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	// Bounded completion-boundary reproduction, not a timing or stress benchmark.
	for iteration := 0; iteration < 64; iteration++ {
		op, _, err := writer.CreateOperation(ctx, ops.Intent{Kind: ops.RestoreTest, App: "example", SecretRef: "input1"}, "operator", fmt.Sprintf("snapshot%d", iteration))
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.TransitionOperation(ctx, op.ID, ops.Queued, ops.Preflight); err != nil {
			t.Fatal(err)
		}
		receipt := restore.Receipt{OperationID: op.ID, Source: restore.RestoreSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{Recoverability: true, BindingID: "b1", Epoch: "e1", TXID: 7}}, ToolVersion: restore.LitestreamVersion, RequestedTXID: 7, RecoveredTXID: 7, ObservedAt: time.Now().UTC(), Schema: restore.SchemaObservation{State: restore.VerifiedSchema, Marker: "v1", CatalogSHA256: strings.Repeat("a", 64)}, LossWindow: restore.LossWindow{State: restore.LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed", PositionEvidence: "remote_dry_run_and_restore"}
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errors := make(chan error, 8)
		var workers sync.WaitGroup
		for worker := 0; worker < 8; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				for read := 0; read < 8; read++ {
					outcome, err := reader.ReadTaskOutcome(ctx, op.ID)
					if err != nil {
						errors <- err
						return
					}
					if outcome != nil && string(outcome.Receipt) != string(raw) {
						errors <- fmt.Errorf("wrong terminal receipt")
						return
					}
				}
			}()
		}
		close(start)
		if err := writer.CompleteTask(ctx, op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
			t.Fatal(err)
		}
		workers.Wait()
		close(errors)
		for err := range errors {
			t.Fatalf("iteration %d: successful completion invalidated snapshot: %v", iteration, err)
		}
		outcome, err := reader.ReadTaskOutcome(ctx, op.ID)
		if err != nil || outcome == nil || string(outcome.Receipt) != string(raw) {
			t.Fatalf("terminal outcome missing: %+v %v", outcome, err)
		}
	}
}
