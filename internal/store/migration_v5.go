package store

import (
	"context"
	"database/sql"

	"github.com/ShaulLavo/brine/internal/data"
)

func migrateDataEvidence(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE data_schema_definitions(database_id TEXT NOT NULL REFERENCES data_databases(id),marker TEXT NOT NULL,catalog_sha256 TEXT NOT NULL,PRIMARY KEY(database_id,marker));
 CREATE TRIGGER data_schema_no_update BEFORE UPDATE ON data_schema_definitions BEGIN SELECT RAISE(ABORT,'immutable schema definition'); END;
 CREATE TRIGGER data_schema_no_delete BEFORE DELETE ON data_schema_definitions BEGIN SELECT RAISE(ABORT,'immutable schema definition'); END;
 CREATE TABLE data_allocations(database_id TEXT PRIMARY KEY REFERENCES data_databases(id),canonical BLOB NOT NULL CHECK(length(canonical)<=4096));
 CREATE TRIGGER data_allocations_no_update BEFORE UPDATE ON data_allocations BEGIN SELECT RAISE(ABORT,'immutable allocation'); END;
 CREATE TRIGGER data_allocations_no_delete BEFORE DELETE ON data_allocations BEGIN SELECT RAISE(ABORT,'immutable allocation'); END;
 CREATE TABLE data_writer_history(database_id TEXT NOT NULL REFERENCES data_databases(id),operation_id TEXT NOT NULL,PRIMARY KEY(database_id,operation_id));
 CREATE TRIGGER data_writer_history_no_update BEFORE UPDATE ON data_writer_history BEGIN SELECT RAISE(ABORT,'immutable writer history'); END;
 CREATE TRIGGER data_writer_history_no_delete BEFORE DELETE ON data_writer_history BEGIN SELECT RAISE(ABORT,'immutable writer history'); END;
 CREATE TABLE data_writer_starts(id INTEGER PRIMARY KEY AUTOINCREMENT,operation_id TEXT NOT NULL REFERENCES operations(id),plan_id TEXT NOT NULL REFERENCES plans(id),incarnation_id TEXT NOT NULL REFERENCES data_incarnations(id),desired_hash TEXT NOT NULL,cleared INTEGER NOT NULL DEFAULT 0 CHECK(cleared IN (0,1)));
 CREATE UNIQUE INDEX data_one_pending_writer_operation ON data_writer_starts(operation_id) WHERE cleared=0;
 CREATE UNIQUE INDEX data_one_pending_writer ON data_writer_starts(incarnation_id) WHERE cleared=0;
 CREATE TRIGGER data_writer_starts_identity BEFORE UPDATE OF operation_id,plan_id,incarnation_id,desired_hash ON data_writer_starts BEGIN SELECT RAISE(ABORT,'immutable writer intent'); END;
 CREATE TRIGGER data_writer_starts_no_reopen BEFORE UPDATE OF cleared ON data_writer_starts WHEN OLD.cleared=1 BEGIN SELECT RAISE(ABORT,'cleared writer intent'); END;
 CREATE TRIGGER data_writer_starts_no_delete BEFORE DELETE ON data_writer_starts BEGIN SELECT RAISE(ABORT,'durable writer intent'); END;
 UPDATE schema_version SET version=5;
 `)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_schema_definitions SELECT id,?,? FROM data_databases", data.EmptyMarker, data.EmptyCatalogSHA256)
	return err
}
