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
	// Build the preceding real schema by withdrawing only the new publication
	// tables from this genuine completed-point fixture. The registry then runs
	// the production 23-to-24 path rather than a standalone table constructor.
	tx, cancel, err := state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DROP TABLE data_native_initial_stage_events;
DROP TABLE data_native_initial_stage_intents;
DROP TABLE data_module_publication_effect_events;
DROP TABLE data_module_publication_effects;
DROP TABLE data_module_publication_approvals;
DROP TABLE data_module_publication_target_intents;
UPDATE schema_version SET version=23;`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"data_native_material_inputs", "data_native_material_receipts", "data_native_prepared_point_bundles"} {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatal("actual23/22 predecessor schema is missing", table, err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = state.migrate(ctx); err != nil {
		t.Fatal("registered23-to24 migration refused actual retained point", err)
	}
	var version int
	if err = state.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("current schema was not registered after publication migration", version, err)
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
