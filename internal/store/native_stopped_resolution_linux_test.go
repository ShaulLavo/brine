//go:build linux

package store

import (
	"context"
	"errors"
	"strings"
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

func TestStoppedOwnerCapturePhaseRefusesOtherIssuedEffects(t *testing.T) {
	for _, effect := range []string{"upload", "point", "capture"} {
		t.Run(effect, func(t *testing.T) { testStoppedOwnerEffectRefusal(t, effect) })
	}
}

func testStoppedOwnerEffectRefusal(t *testing.T, effect string) {
	t.Helper()
	state, prep, life, op := nativePreparationFixture(t)
	ctx := context.Background()
	if _, err := state.RecordNativeStoppedPreparation(ctx, prep, life, nativeAbsentStopFixture()); err != nil {
		t.Fatal(err)
	}
	events, err := readNativePreparationEvents(ctx, state.db, prep.intent, prep.raw)
	if err != nil {
		t.Fatal(err)
	}
	switch effect {
	case "upload":
		_, err = state.db.ExecContext(ctx, "INSERT INTO data_native_prepared_upload_intents VALUES(?,?,?,?)", prep.PointID(), strings.Repeat("a", 64), strings.Repeat("b", 64), []byte("{}"))
	case "point":
		_, err = state.db.ExecContext(ctx, "INSERT INTO data_native_prepared_points VALUES(?,?,?,?)", prep.PointID(), prep.intent.BindingID, prep.intent.EpochID, []byte("{}"))
	case "capture":
		_, err = state.db.ExecContext(ctx, "INSERT INTO data_native_capture_inputs VALUES(?,?,?,?,?,?)", strings.Repeat("a", 32), op.ID, prep.intent.BindingID, prep.intent.EpochID, "exact", []byte("{}"))
	}
	if err != nil {
		t.Fatal("effect fixture setup", err)
	}
	if _, err := nativePreparedNeedsCapture(ctx, state.db, prep.intent, events); !errors.Is(err, ErrConflict) {
		t.Fatal("issued effect admitted owner-only capture", effect, err)
	}
}
