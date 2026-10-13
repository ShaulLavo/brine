//go:build linux

package store

import (
	"context"
	"testing"
)

func dropManagedAndPublicationMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	for _, table := range []string{"data_native_initial_stage_events", "data_native_initial_stage_intents", "data_module_publication_effect_events", "data_module_publication_effects", "data_module_publication_approvals", "data_module_publication_target_intents", "data_engine_route_heads", "data_engine_route_states", "data_engine_desired_heads", "data_engine_desired_states", "data_engine_frontend_heads", "data_engine_frontend_permits", "data_engine_preparations", "data_module_publication_resolutions", "data_module_publication_proofs", "data_module_publication_cursor_records", "data_module_publication_cursors", "data_module_publication_claims", "data_module_publication_reviews"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM transitions WHERE kind IN ('module_publish_apply','module_publish_resolve')"); err != nil {
		t.Fatal(err)
	}
}
