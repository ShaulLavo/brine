package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRetirementMigrationPreservesOriginalImmutableJournal(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	ctx := context.Background()
	_, err = database.ExecContext(ctx, `CREATE TABLE operations(id TEXT PRIMARY KEY);INSERT INTO operations VALUES('original');CREATE TABLE data_native_backup_resolution_events(operation_id TEXT NOT NULL REFERENCES operations(id),stage TEXT NOT NULL CHECK(stage IN ('claimed','units_issued','observed','completed')),canonical BLOB NOT NULL,PRIMARY KEY(operation_id,stage));`)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"exact":"original canonical bytes"}`)
	for _, stage := range []string{"claimed", "units_issued", "observed", "completed"} {
		if _, err = database.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES('original',?,?)", stage, original); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateWorkspaceRetirementJournal(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"claimed", "units_issued", "observed", "completed"} {
		var raw []byte
		if err = database.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id='original' AND stage=?", stage).Scan(&raw); err != nil || !bytes.Equal(raw, original) {
			t.Fatal("original journal changed", stage, err)
		}
	}
	for _, stage := range []string{"upload_retire", "workspace_retire:" + strings.Repeat("a", 32)} {
		if _, err = database.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES('original',?,?)", stage, original); err != nil {
			t.Fatal("closed retirement stage refused", err)
		}
	}
	for _, stage := range []string{"workspace_retire:", "workspace_retire:" + strings.Repeat("g", 32), "workspace_retire:" + strings.Repeat("a", 31), "workspace_retire:" + strings.Repeat("a", 33), "arbitrary"} {
		if _, err = database.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES('original',?,?)", stage, original); err == nil {
			t.Fatal("unbounded stage admitted", stage)
		}
	}
	if _, err = database.ExecContext(ctx, "UPDATE data_native_backup_resolution_events SET canonical='changed'"); err == nil {
		t.Fatal("immutable journal became mutable")
	}
	if _, err = database.ExecContext(ctx, "DELETE FROM data_native_backup_resolution_events"); err == nil {
		t.Fatal("retained journal became deletable")
	}
}
