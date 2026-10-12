//go:build linux

package store

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeMigration17PreservesExistingControlAndAddsStableDependencies(t *testing.T) {
	ctx, state := context.Background(), openTest(t)
	reserved, err := state.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "fixture"}, "requester", "retained-operation")
	if err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := state.db.QueryRowContext(ctx, "SELECT canonical FROM data_databases WHERE id=?", reserved.Database.DatabaseID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	dropNativeCaptureMigrationFixture(t, ctx, state)
	if _, err := state.db.ExecContext(ctx, "UPDATE schema_version SET version=16"); err != nil {
		t.Fatal(err)
	}
	if err := state.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var canonical []byte
	if err := state.db.QueryRowContext(ctx, "SELECT canonical FROM data_databases WHERE id=?", reserved.Database.DatabaseID).Scan(&canonical); err != nil || !bytes.Equal(original, canonical) {
		t.Fatal("native migration changed SQLite canonical bytes", err)
	}
	retained, err := state.GetOperation(ctx, op.ID)
	if err != nil || retained != op {
		t.Fatal("native migration changed a recorded operation", err)
	}
	for _, table := range []string{"data_native_repository_intents", "data_native_repository_results", "data_native_repository_init_events", "data_native_repository_leases", "data_native_checkpoints", "data_native_capture_inputs", "data_native_capture_events", "data_native_backup_policies", "data_native_backup_revision_plans", "data_native_backup_revision_claims", "data_native_backup_revision_events", "data_native_recovery_deliveries", "data_native_recovery_retentions"} {
		var count int
		if err := state.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatal("stable prerequisite table missing", table, err)
		}
	}
	rows, err := state.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		_ = rows.Close()
		t.Fatal("migration left a broken foreign key")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRepositoryLeaseAndCaptureJournalRefuseReuseAndRewrite(t *testing.T) {
	ctx, state := context.Background(), openTest(t)
	reserved, err := state.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "fixture"}, "requester", "lease-fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, epoch := reserved.Replica.BindingID, reserved.Replica.EpochID
	lease := strings.Repeat("a", 32)
	if _, err := state.db.ExecContext(ctx, "INSERT INTO data_native_repository_leases VALUES(?,?,?,?,?,?)", lease, op.ID, binding, epoch, "held", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "INSERT INTO data_native_repository_leases VALUES(?,?,?,?,?,?)", strings.Repeat("b", 32), op.ID, binding, epoch, "held", []byte(`{}`)); err == nil {
		t.Fatal("repository admitted two writers")
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE data_native_repository_leases SET canonical=? WHERE id=?", []byte(`{"changed":true}`), lease); err == nil {
		t.Fatal("repository lease identity mutable")
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE data_native_repository_leases SET state='released' WHERE id=?", lease); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE data_native_repository_leases SET state='held' WHERE id=?", lease); err == nil {
		t.Fatal("released lease revived")
	}
	capture := strings.Repeat("c", 32)
	if _, err := state.db.ExecContext(ctx, "INSERT INTO data_native_capture_inputs VALUES(?,?,?,?,?,?)", capture, op.ID, binding, epoch, "online", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "INSERT INTO data_native_capture_events VALUES(?,?,?,?,?)", capture, 2, "prepared", strings.Repeat("d", 64), []byte(`{}`)); err == nil {
		t.Fatal("capture journal skipped a sequence")
	}
	if _, err := state.db.ExecContext(ctx, "INSERT INTO data_native_capture_events VALUES(?,?,?,?,?)", capture, 1, "prepared", strings.Repeat("d", 64), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE data_native_capture_events SET stage='completed' WHERE capture_id=?", capture); err == nil {
		t.Fatal("capture journal rewritten")
	}
}
