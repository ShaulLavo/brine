package store

import (
	"context"
	"database/sql"
)

func migrateReplicaRevisions(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_replica_revisions(id TEXT PRIMARY KEY,binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),stage TEXT NOT NULL CHECK(stage IN ('stored','prepared','stop_issued','stopped','committed','start_issued','active')),canonical BLOB NOT NULL CHECK(length(canonical)<=262144));
 CREATE UNIQUE INDEX data_one_pending_revision ON data_replica_revisions(binding_id) WHERE stage != 'active';
 CREATE TRIGGER data_revision_identity BEFORE UPDATE OF id,binding_id ON data_replica_revisions BEGIN SELECT RAISE(ABORT,'immutable replica revision identity'); END;
 CREATE TRIGGER data_revision_no_delete BEFORE DELETE ON data_replica_revisions BEGIN SELECT RAISE(ABORT,'durable replica revision'); END;
 UPDATE schema_version SET version=9;
 `)
	return err
}
