package store

import (
	"context"
	"database/sql"
)

func migrateData(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_incarnations (id TEXT PRIMARY KEY, app TEXT NOT NULL UNIQUE);
CREATE TABLE data_databases (id TEXT PRIMARY KEY, incarnation_id TEXT NOT NULL REFERENCES data_incarnations(id), name TEXT NOT NULL, root TEXT NOT NULL, relative_directory TEXT NOT NULL, canonical BLOB NOT NULL, UNIQUE(incarnation_id,name), UNIQUE(root,relative_directory));
CREATE TABLE data_admissions (database_id TEXT NOT NULL REFERENCES data_databases(id), version INTEGER NOT NULL CHECK(version>0), policy_hash TEXT NOT NULL, PRIMARY KEY(database_id,version));
CREATE TABLE data_replica_bindings (id TEXT PRIMARY KEY, database_id TEXT NOT NULL UNIQUE REFERENCES data_databases(id), epoch_id TEXT NOT NULL UNIQUE, endpoint TEXT NOT NULL, bucket TEXT NOT NULL, prefix TEXT NOT NULL, canonical BLOB NOT NULL, UNIQUE(endpoint,bucket,prefix));
CREATE TABLE data_replica_commits (binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id), version INTEGER NOT NULL CHECK(version>0), canonical BLOB NOT NULL, PRIMARY KEY(binding_id,version));
CREATE TABLE data_fences (id TEXT PRIMARY KEY, database_id TEXT NOT NULL REFERENCES data_databases(id), incarnation_id TEXT NOT NULL REFERENCES data_incarnations(id), operation_id TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('held','released')));
CREATE UNIQUE INDEX data_one_held_fence ON data_fences(database_id) WHERE state='held';
CREATE TRIGGER data_fence_identity BEFORE UPDATE OF id,database_id,incarnation_id,operation_id ON data_fences BEGIN SELECT RAISE(ABORT,'immutable fence identity'); END;
CREATE TRIGGER data_fence_no_rehold BEFORE UPDATE OF state ON data_fences WHEN OLD.state='released' BEGIN SELECT RAISE(ABORT,'released fence'); END;
CREATE TRIGGER data_fence_no_delete BEFORE DELETE ON data_fences BEGIN SELECT RAISE(ABORT,'durable fence'); END;
CREATE TABLE data_credential_records (id TEXT PRIMARY KEY, binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id), kind TEXT NOT NULL CHECK(kind IN ('plan','receipt')), canonical BLOB NOT NULL CHECK(length(canonical)<=65536));
UPDATE schema_version SET version=4;
`)
	if err != nil {
		return err
	}
	for _, table := range []string{"data_incarnations", "data_databases", "data_admissions", "data_replica_bindings", "data_replica_commits", "data_credential_records"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, "CREATE TRIGGER "+table+"_no_"+action+" BEFORE "+action+" ON "+table+" BEGIN SELECT RAISE(ABORT,'immutable data identity'); END;"); err != nil {
				return err
			}
		}
	}
	return nil
}
