//go:build linux

package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func TestSQLiteRemovalMigrationPreservesPopulatedFloors(t *testing.T) {
	for _, floor := range []int{35, 36} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) { checkSQLiteRemovalMigrationFloor(t, floor) })
	}
}

func checkSQLiteRemovalMigrationFloor(t *testing.T, floor int) {
	t.Helper()
	fx := newPrepFixture(t)
	fx.prepared(prepID("1"))
	if _, _, err := fx.prepare(newPrepHostStopped(fx), nil, prepID("2"), prepID("1")); err != nil {
		t.Fatal(err)
	}
	task, in := fx.preflight(sqliteprep.AbandonTask, prepID("2"), "")
	if _, err := fx.service(newPrepHostStopped(fx), nil).Abandon(t.Context(), task, in); err != nil {
		t.Fatal(err)
	}
	queries := []string{"SELECT * FROM operations ORDER BY id", "SELECT * FROM events ORDER BY operation_id,seq", "SELECT * FROM operation_outcomes ORDER BY operation_id", "SELECT * FROM data_sqlite_preparations ORDER BY id", "SELECT * FROM data_sqlite_point_consumers ORDER BY preparation_id", "SELECT * FROM data_sqlite_consumer_completions ORDER BY preparation_id", "SELECT * FROM data_fences ORDER BY id", "SELECT * FROM data_sqlite_preparation_events ORDER BY preparation_id,seq", "SELECT * FROM data_sqlite_prepared_points ORDER BY preparation_id"}
	before := dropRowsFingerprint(t, fx.s, queries)
	makeExactV36RemovalFixture(t, fx.s)
	if floor == 35 {
		makeExactV35DropFixture(t, fx.s)
	}
	if after := dropRowsFingerprint(t, fx.s, queries); after != before {
		t.Fatal("fixture changed immutable rows")
	}
	if floor == 36 {
		execRemovalFixture(t, fx.s, "INSERT INTO transitions VALUES('drop_apply','queued','preflight')")
	}
	dir := fx.s.dir
	if err := fx.s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(t.Context(), dir)
	var version *SchemaError
	if !errors.As(err, &version) || version.Version != floor {
		if ro != nil {
			_ = ro.Close()
		}
		t.Fatal("old read-only schema admitted", err)
	}
	s, err := OpenContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if after := dropRowsFingerprint(t, s, queries); after != before {
		t.Fatal("migration changed immutable rows")
	}
	var broken, latest int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&broken); err != nil || broken != 0 {
		t.Fatal(broken, err)
	}
	if err = s.db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&latest); err != nil || latest != 37 {
		t.Fatal(latest, err)
	}
	if _, err = s.ReadSQLiteTaskOutcome(t.Context(), task.OperationID); err != nil {
		t.Fatal("007 completed abandonment unreadable", err)
	}
	var triggers string
	if err = s.db.QueryRowContext(t.Context(), "SELECT group_concat(name,',') FROM (SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='operations' ORDER BY name)").Scan(&triggers); err != nil || triggers != "cdn_reserved_app,legal_transition,operation_identity" {
		t.Fatal(triggers, err)
	}
	var retainedDropTransitions int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM transitions WHERE kind='drop_apply'").Scan(&retainedDropTransitions); err != nil || retainedDropTransitions != floor-35 {
		t.Fatal("lost schema-only predecessor transition", retainedDropTransitions, err)
	}
	for _, kind := range []ops.Kind{ops.SQLiteRetire, ops.SQLiteRetireResolve} {
		var count int
		if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM transitions WHERE kind=?", kind).Scan(&count); err != nil || count == 0 {
			t.Fatal("lost transitions", kind, err)
		}
	}
}

func TestSQLiteRemovalMigrationRollback(t *testing.T) {
	s := openTest(t)
	makeExactV36RemovalFixture(t, s)
	execRemovalFixture(t, s, "CREATE TABLE data_sqlite_archive_attempts(conflict TEXT)")
	dir := s.dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenContext(t.Context(), dir)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("conflicting migration committed")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version, partial int
	if err = db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 36 {
		t.Fatal(version, err)
	}
	if err = db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE name IN ('operations_v37','data_sqlite_archive_claims','data_sqlite_retire_inputs')").Scan(&partial); err != nil || partial != 0 {
		t.Fatal("partial migration", partial, err)
	}
}

func TestSQLiteRemovalTaskSQLShapes(t *testing.T) {
	s := openTest(t)
	hash := strings.Repeat("c", 64)
	for _, row := range [][]any{
		{"bad1", nil, "key1", "queued", "sqlite_retire", "example", "short", ""},
		{"bad2", nil, "key2", "preparing", "sqlite_retire", "example", hash, ""},
		{"bad3", nil, "key3", "queued", "sqlite_retire_resolve", "example", hash, ""},
		{"bad4", nil, "key4", "queued", "sqlite_retire", "example", hash, "original"},
		{"bad5", "sha256:" + hash, "key5", "queued", "sqlite_retire", "example", hash, ""},
	} {
		if _, err := s.db.ExecContext(t.Context(), "INSERT INTO operations VALUES(?,?,'requester',?,?,'t','t',?,?,?,?)", row...); err == nil {
			t.Fatal("invalid task shape accepted", row)
		}
	}
}
