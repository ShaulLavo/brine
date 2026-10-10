package store

import (
	"context"
	"database/sql"
)

func migrateRestorePoints(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_restore_points(id TEXT PRIMARY KEY,binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),epoch_id TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
 CREATE TRIGGER data_restore_points_no_update BEFORE UPDATE ON data_restore_points BEGIN SELECT RAISE(ABORT,'immutable restore point'); END;
 CREATE TRIGGER data_restore_points_no_delete BEFORE DELETE ON data_restore_points BEGIN SELECT RAISE(ABORT,'immutable restore point'); END;
 UPDATE schema_version SET version=6;
 `)
	return err
}
