//go:build linux

package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/liverestore"
)

func TestNativeRestoreReviewBeforeTransitionIsRecorded(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	input, cursor, err := readNativeRestoreTransitionInput(ctx, f.store.db, f.input, liverestore.TransitionIntent)
	if err != nil || input.SHA256() != f.input.SHA256() || cursor.Sequence != 0 {
		t.Fatal("retained target before transition cannot be reviewed", err)
	}
	for _, stage := range []liverestore.Stage{liverestore.Claimed, liverestore.RestoreIntent, liverestore.Verified, liverestore.Transitioned, liverestore.FinishIntent, liverestore.Completed} {
		if _, _, err := readNativeRestoreTransitionInput(ctx, f.store.db, f.input, stage); err == nil {
			t.Fatalf("missing transition accepted at %s", stage)
		}
	}
	if _, err := f.store.ResumeNativeRestoreIncomingRepository(ctx, nil); err == nil {
		t.Fatal("missing resolver capability acquired an incoming repository")
	}
	if err := f.store.PrepareResolvedNativeRestoreTransition(ctx, nil, nil, NativeRestoreTransitionObservation{}); err == nil {
		t.Fatal("missing resolver capability admitted a transition")
	}
	var count int
	if err := f.store.db.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_transition_inputs").Scan(&count); err != nil || count != 0 {
		t.Fatal("inspection created a transition", err, count)
	}
	recorded := claimTransitionFixture(t, f)
	input, cursor, err = readNativeRestoreTransitionInput(ctx, f.store.db, f.input, liverestore.TransitionIntent)
	if err != nil || input.SHA256() != f.input.SHA256() || cursor != recorded {
		t.Fatal("recorded transition was replaced by a synthetic pending input", err)
	}
}
