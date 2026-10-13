package store

import (
	"context"
	"database/sql"
)

func migrateWorkspaceRetirementJournal(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE data_native_backup_resolution_events_v25(
 operation_id TEXT NOT NULL REFERENCES operations(id),
 stage TEXT NOT NULL CHECK(stage IN ('claimed','units_issued','observed','completed','upload_retire') OR
 (length(stage)=49 AND substr(stage,1,17)='workspace_retire:' AND substr(stage,18) NOT GLOB '*[^0-9a-f]*')),
 canonical BLOB NOT NULL CHECK(length(canonical)<=32768),PRIMARY KEY(operation_id,stage));
INSERT INTO data_native_backup_resolution_events_v25 SELECT * FROM data_native_backup_resolution_events;
DROP TABLE data_native_backup_resolution_events;
ALTER TABLE data_native_backup_resolution_events_v25 RENAME TO data_native_backup_resolution_events;
CREATE TRIGGER data_native_backup_resolution_events_no_update BEFORE UPDATE ON data_native_backup_resolution_events BEGIN SELECT RAISE(ABORT,'immutable native backup resolution'); END;
CREATE TRIGGER data_native_backup_resolution_events_no_delete BEFORE DELETE ON data_native_backup_resolution_events BEGIN SELECT RAISE(ABORT,'retained native backup resolution'); END;
`)
	return err
}
