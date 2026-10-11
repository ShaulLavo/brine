package store

import (
	"context"
	"database/sql"
)

func migrateEngineOwnership(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_engine_kinds(database_id TEXT PRIMARY KEY REFERENCES data_databases(id),engine TEXT NOT NULL CHECK(engine IN ('sqlite','spacetimedb')));
INSERT INTO data_engine_kinds SELECT id,'sqlite' FROM data_databases;
CREATE TRIGGER data_engine_kinds_no_update BEFORE UPDATE ON data_engine_kinds BEGIN SELECT RAISE(ABORT,'immutable engine identity'); END;
CREATE TRIGGER data_engine_kinds_no_delete BEFORE DELETE ON data_engine_kinds BEGIN SELECT RAISE(ABORT,'immutable engine identity'); END;
CREATE TABLE data_engine_runtime_commits(binding_id TEXT NOT NULL REFERENCES data_replica_bindings(id),version INTEGER NOT NULL CHECK(version>0),canonical BLOB NOT NULL CHECK(length(canonical)<=65536),PRIMARY KEY(binding_id,version));
CREATE TRIGGER data_engine_runtime_no_update BEFORE UPDATE ON data_engine_runtime_commits BEGIN SELECT RAISE(ABORT,'immutable engine runtime'); END;
CREATE TRIGGER data_engine_runtime_no_delete BEFORE DELETE ON data_engine_runtime_commits BEGIN SELECT RAISE(ABORT,'immutable engine runtime'); END;
CREATE TABLE data_engine_modules(database_id TEXT NOT NULL REFERENCES data_databases(id),marker TEXT NOT NULL,canonical BLOB NOT NULL CHECK(length(canonical)<=16384),PRIMARY KEY(database_id,marker));
CREATE TRIGGER data_engine_modules_no_update BEFORE UPDATE ON data_engine_modules BEGIN SELECT RAISE(ABORT,'immutable engine module'); END;
CREATE TRIGGER data_engine_modules_no_delete BEFORE DELETE ON data_engine_modules BEGIN SELECT RAISE(ABORT,'immutable engine module'); END;
UPDATE schema_version SET version=15;
`)
	return err
}
