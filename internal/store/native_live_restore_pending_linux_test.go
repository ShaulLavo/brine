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
	var count int
	if err := f.store.db.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_transition_inputs").Scan(&count); err != nil || count != 0 {
		t.Fatal("inspection created a transition", err, count)
	}
}
