package store

import (
	"context"
	"database/sql"
)

func migrateNativeArchiveIncomingTrees(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_native_archive_trees_v31(
 archive_id TEXT NOT NULL REFERENCES data_archive_plans(archive_id),
 binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),
 transition_id TEXT NOT NULL,
 canonical BLOB NOT NULL CHECK(length(canonical)<=65536),
 PRIMARY KEY(archive_id,transition_id)
);
INSERT INTO data_native_archive_trees_v31 SELECT * FROM data_native_archive_trees;
DROP TABLE data_native_archive_trees;
ALTER TABLE data_native_archive_trees_v31 RENAME TO data_native_archive_trees;
CREATE TRIGGER data_native_archive_trees_known_source BEFORE INSERT ON data_native_archive_trees
WHEN NOT EXISTS (SELECT 1 FROM data_engine_transition_inputs WHERE id=NEW.transition_id AND binding_id=NEW.binding_id)
 AND NOT EXISTS (SELECT 1 FROM data_native_repository_intents WHERE id=NEW.transition_id AND binding_id=NEW.binding_id)
BEGIN SELECT RAISE(ABORT,'unknown native archive tree source'); END;
CREATE TRIGGER data_native_archive_trees_no_update BEFORE UPDATE ON data_native_archive_trees BEGIN SELECT RAISE(ABORT,'immutable native archive closure'); END;
CREATE TRIGGER data_native_archive_trees_no_delete BEFORE DELETE ON data_native_archive_trees BEGIN SELECT RAISE(ABORT,'immutable native archive closure'); END;
UPDATE schema_version SET version=31;
`)
	return err
}
