package store

import (
	"context"
	"testing"
)

func TestNativeArchiveTransitionClosureRefusesAnUnfinishedRetainedTransition(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	closed, err := f.store.ReadNativeArchiveTransitionClosure(ctx, f.runtime.BindingID)
	if err != nil || len(closed) != 0 {
		t.Fatal("empty actual transition closure refused", err)
	}
	claimTransitionFixture(t, f)
	if _, err = f.store.ReadNativeArchiveTransitionClosure(ctx, f.runtime.BindingID); err == nil {
		t.Fatal("unfinished transition omitted from archive closure")
	}
	var count int
	if err = f.store.db.QueryRowContext(ctx, "SELECT count(*) FROM data_archive_operations").Scan(&count); err != nil || count != 0 {
		t.Fatal("readonly closure claimed an archive", err)
	}
}
