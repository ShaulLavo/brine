//go:build linux

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/liverestore"
)

func storedNativeRestoreJournalFixture(t *testing.T) (nativeTransitionFixture, liverestore.Plan, liverestore.Event) {
	t.Helper()
	f, p := storedNativeLiveRestoreFixture(t)
	ctx := context.Background()
	if err := f.store.SaveNativeLiveRestorePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	s := p.Spec()
	attempt := liverestore.Attempt{PlanID: p.ID(), OperationID: f.input.Common().Scope.ApplyOperationID, ConsumerSHA256: strings.Repeat("c", 64),
		PreparedPointID: s.Safety.PointID, SelectedPointID: s.Selected.PointID, FenceID: s.Safety.FenceID, FenceOwnerOpID: s.Safety.FenceOwnerOpID,
		BindingID: s.Facts.Head.BindingID, SourceEpochID: s.Facts.Head.EpochID, IncomingEpochID: f.runtime.EpochID}
	claim, err := liverestore.BeginAttempt(p, attempt)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the private journal half only. This fixture does not claim an actual17
	// point, create protected20 proofs, or admit a host effect.
	_, err = f.store.db.ExecContext(ctx, "INSERT INTO data_native_live_restore_claims VALUES(?,?,?,?,?,?)", attempt.OperationID,
		attempt.PlanID, attempt.PreparedPointID, attempt.BindingID, attempt.IncomingEpochID, claim.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_native_live_restore_events VALUES(?,?,?,?)", attempt.OperationID, claim.Sequence(), claim.Stage(), claim.Bytes()); err != nil {
		t.Fatal(err)
	}
	return f, p, claim
}

func appendRestoreJournalFixture(ctx context.Context, f nativeTransitionFixture, old, next liverestore.Event) error {
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = f.store.appendNativeLiveRestoreEventTx(ctx, tx, f.preparation, old, next); err != nil {
		return err
	}
	return tx.Commit()
}

func TestNativeLiveRestoreJournalHasOneCursorWinnerAndRetainsPendingIntent(t *testing.T) {
	f, p, claim := storedNativeRestoreJournalFixture(t)
	ctx := context.Background()
	next, err := liverestore.RecordIntent(p, claim, liverestore.RestoreIntent)
	if err != nil {
		t.Fatal(err)
	}
	if err = appendRestoreJournalFixture(ctx, f, claim, next); err != nil {
		t.Fatal(err)
	}
	if err = appendRestoreJournalFixture(ctx, f, claim, next); err == nil {
		t.Fatal("stale cursor wrote the same unknown intent again")
	}
	retained, err := f.store.ReadNativeLiveRestoreProgress(ctx, claim.Attempt().OperationID)
	if err != nil || !retained.Pending() || retained.SHA256() != next.SHA256() || retained.Attempt() != claim.Attempt() {
		t.Fatal("restart lost original intent/consumer scope", err)
	}
	if _, err = f.store.db.ExecContext(ctx, "UPDATE data_native_live_restore_events SET stage='completed' WHERE operation_id=?", claim.Attempt().OperationID); err == nil {
		t.Fatal("intent rewritten as completion")
	}
	if _, err = f.store.db.ExecContext(ctx, "DELETE FROM data_native_live_restore_claims WHERE operation_id=?", claim.Attempt().OperationID); err == nil {
		t.Fatal("original consumer scope discarded")
	}
}

func TestNativeLiveRestoreJournalRefusesSequenceGapsAndReleasedGuard(t *testing.T) {
	f, p, claim := storedNativeRestoreJournalFixture(t)
	ctx := context.Background()
	next, err := liverestore.RecordIntent(p, claim, liverestore.RestoreIntent)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	if err = appendRestoreJournalFixture(ctx, f, claim, next); err == nil {
		t.Fatal("released launch guard wrote intent")
	}
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_native_live_restore_events VALUES(?,?,?,?)", claim.Attempt().OperationID,
		next.Sequence()+1, next.Stage(), next.Bytes()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReadNativeLiveRestoreProgress(ctx, claim.Attempt().OperationID); err == nil {
		t.Fatal("indexed sequence gap admitted")
	}
}
