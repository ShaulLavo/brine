package store

import (
	"context"
	"database/sql"
)

func migrateNativeInitialStages(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_native_initial_stage_intents(
 nonce TEXT PRIMARY KEY, point_id TEXT NOT NULL REFERENCES data_native_preparations(id),
 operation_id TEXT NOT NULL REFERENCES operations(id), canonical BLOB NOT NULL CHECK(length(canonical)<=16384));
CREATE TABLE data_native_initial_stage_events(
 nonce TEXT NOT NULL REFERENCES data_native_initial_stage_intents(nonce), sequence INTEGER NOT NULL CHECK(sequence>0),
 canonical BLOB NOT NULL CHECK(length(canonical)<=16384), PRIMARY KEY(nonce,sequence));
CREATE TRIGGER data_native_initial_stage_intents_no_update BEFORE UPDATE ON data_native_initial_stage_intents BEGIN SELECT RAISE(ABORT,'immutable initial stage intent'); END;
CREATE TRIGGER data_native_initial_stage_intents_no_delete BEFORE DELETE ON data_native_initial_stage_intents BEGIN SELECT RAISE(ABORT,'retained initial stage intent'); END;
CREATE TRIGGER data_native_initial_stage_events_no_update BEFORE UPDATE ON data_native_initial_stage_events BEGIN SELECT RAISE(ABORT,'immutable initial stage event'); END;
CREATE TRIGGER data_native_initial_stage_events_no_delete BEFORE DELETE ON data_native_initial_stage_events BEGIN SELECT RAISE(ABORT,'retained initial stage event'); END;
UPDATE schema_version SET version=26;
`)
	return err
}
