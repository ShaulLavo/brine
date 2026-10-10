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
 CREATE TABLE data_credential_rotations(plan_id TEXT PRIMARY KEY,binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),stage TEXT NOT NULL CHECK(stage IN ('prepared','stop_issued','stopped','committed','start_issued','active','verified')),canonical BLOB NOT NULL CHECK(length(canonical)<=32768));
 CREATE UNIQUE INDEX data_one_pending_rotation ON data_credential_rotations(binding_id) WHERE stage NOT IN ('active','verified');
 CREATE TRIGGER data_rotation_identity BEFORE UPDATE OF plan_id,binding_id ON data_credential_rotations BEGIN SELECT RAISE(ABORT,'immutable rotation identity'); END;
 CREATE TRIGGER data_rotation_no_delete BEFORE DELETE ON data_credential_rotations BEGIN SELECT RAISE(ABORT,'durable credential rotation'); END;
 UPDATE schema_version SET version=6;
 `)
	return err
}
