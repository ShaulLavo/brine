package replicapermits

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
)

type attemptStoreFake struct {
	*permitStoreFake
	attempts int
}

func (f *attemptStoreFake) RecordWriterAttempt(_ context.Context, id data.AppIncarnationID, op string) error {
	if f.err != nil {
		return f.err
	}
	if id != f.start.Intent.IncarnationID || op != f.start.Intent.OperationID {
		return errors.New("wrong identity")
	}
	f.attempts++
	f.schema.Allocations = nil
	return nil
}
func TestWriterAttemptConsumesAllocationBeforeWriterAndIsIdempotent(t *testing.T) {
	r, state, evidence, id := allocatedWriterFixture(t)
	state.start = store.WriterStartResolution{State: store.WriterStartPending, Intent: &store.WriterStartIntent{IncarnationID: data.AppIncarnationID(id), OperationID: "operation", Desired: state.schema.Desired}}
	f := &attemptStoreFake{permitStoreFake: state}
	a := WriterAttempts{State: f, Operations: evidence}
	if err := replication.WriterPermit(context.Background(), r, id); err != nil {
		t.Fatal(err)
	}
	if err := a.WriterAttempt(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	// No ExecStart occurred. A restart after this crash must not infer an untouched DB.
	if replication.WriterPermit(context.Background(), r, id) == nil {
		t.Fatal("crash preserved untouched allocation authority")
	}
	if err := a.WriterAttempt(context.Background(), id); err != nil {
		t.Fatal("same-operation retry refused", err)
	}
	if f.attempts != 2 {
		t.Fatal("attempt not recorded")
	}
}
func TestWriterAttemptCommittedNoopAndInvalidRefusal(t *testing.T) {
	_, state, evidence, id := allocatedWriterFixture(t)
	f := &attemptStoreFake{permitStoreFake: state}
	a := WriterAttempts{State: f, Operations: evidence}
	state.start.State = store.WriterStartNone
	if err := a.WriterAttempt(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if f.attempts != 0 || evidence.activeReads != 0 {
		t.Fatal("committed restart mutated absence evidence")
	}
	for _, status := range []store.WriterStartState{store.WriterStartInvalid, store.WriterStartPending, "unknown"} {
		state.start.State = status
		if a.WriterAttempt(context.Background(), id) == nil {
			t.Fatal("invalid or incomplete intent admitted", status)
		}
	}
	state.start = store.WriterStartResolution{State: store.WriterStartPending, Intent: &store.WriterStartIntent{IncarnationID: data.AppIncarnationID(id), OperationID: "operation"}}
	evidence.active = false
	if a.WriterAttempt(context.Background(), id) == nil {
		t.Fatal("dead operation admitted")
	}
	evidence.active = true
	evidence.err = errors.New("unknown")
	if a.WriterAttempt(context.Background(), id) == nil {
		t.Fatal("unknown operation admitted")
	}
	if f.attempts != 0 {
		t.Fatal("refused attempts mutated store")
	}
}
