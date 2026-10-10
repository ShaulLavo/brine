package store

import (
	"context"
	"database/sql"
)

func migratePlanInputs(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
 CREATE TABLE plan_inputs(id TEXT PRIMARY KEY REFERENCES plans(id),canonical BLOB NOT NULL CHECK(length(canonical)<=16777216));
 CREATE TRIGGER plan_input_no_update BEFORE UPDATE ON plan_inputs BEGIN SELECT RAISE(ABORT,'immutable plan input'); END;
 CREATE TRIGGER plan_input_no_delete BEFORE DELETE ON plan_inputs BEGIN SELECT RAISE(ABORT,'immutable plan input'); END;
 UPDATE schema_version SET version=10;
 `)
	return err
}
