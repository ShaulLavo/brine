//go:build linux

package store

import (
	"bytes"
	"context"
	"testing"
)

func TestModulePublicationReviewMigrationPreservesActualPreparedPoint(t *testing.T) {
	state, _, point := nativeCompletedPointFixture(t)
	ctx := context.Background()
	var before []byte
	if err := state.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_points WHERE id=?", point.PointID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, cancel, err := state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = migratePublicationReviewAcknowledgements(ctx, tx); err != nil {
		t.Fatal("publication tables did not join the actual native ledger", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err = state.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_points WHERE id=?", point.PointID).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("publication migration changed the retained source point", err)
	}
	for _, table := range []string{"data_module_publication_approvals", "data_module_publication_effects", "data_module_publication_effect_events", "data_module_publication_target_intents"} {
		var count int
		if err = state.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing protected publication table", table, err)
		}
	}
	if _, err = state.ReadNativePreparedPoint(ctx, point.PointID); err != nil {
		t.Fatal("actual point could not be read after publication table creation", err)
	}
}
