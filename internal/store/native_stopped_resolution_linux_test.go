//go:build linux

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestStoppedOriginalIdentitySurvivesProgressWithoutAdmittingUnprovedEffects(t *testing.T) {
	state, preparation, lifetime, op := nativePreparationFixture(t)
	ctx := context.Background()
	if _, err := state.RecordNativeStoppedPreparation(ctx, preparation, lifetime, nativeAbsentStopFixture()); err != nil {
		t.Fatal(err)
	}
	if err := state.CompleteTask(ctx, op.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	original, err := state.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, before, err := nativeBackupOriginal(ctx, state.db, op.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	events, err := readNativePreparationEvents(ctx, state.db, preparation.intent, preparation.raw)
	if err != nil {
		t.Fatal(err)
	}
	if needs, err := nativePreparedNeedsCapture(ctx, state.db, preparation.intent, events); err != nil || !needs {
		t.Fatal("genuine stopped-only claim not recognized", err)
	}
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	// Deliberately append content-free progress only in this fixture. It must
	// preserve original identity without manufacturing completed producer proof.
	for _, stage := range []string{"source", "remote", "verified", "completed"} {
		if err := appendNativePreparationProof(ctx, tx, preparation.intent, preparation.raw, stage, []byte(`{"unproved_fixture":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, _, after, err := nativeBackupOriginal(ctx, state.db, op.ID, original.Requester)
	if err != nil || before != after {
		t.Fatal("new progress rebound immutable original identity", err)
	}
	events, err = readNativePreparationEvents(ctx, state.db, preparation.intent, preparation.raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativePreparedNeedsCapture(ctx, state.db, preparation.intent, events); !errors.Is(err, ErrConflict) {
		t.Fatal("progress without actual upload/producer proof supplied unissued authority", err)
	}
	if _, err := state.ReadNativePreparedPoint(ctx, preparation.PointID()); !errors.Is(err, ErrNotFound) {
		t.Fatal("unproved event sequence manufactured completed point", err)
	}
}
