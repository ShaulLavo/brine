//go:build linux

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

// v34Store returns a populated control store rewound to the exact version 34
// shape: every unrelated table, trigger and row stays as the real migrations
// produced them, and only version 35's own objects are removed.
func v34Store(t *testing.T, conflict string) (string, string) {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	if _, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "token"}, "operator", "secret"); err != nil {
		t.Fatal(err)
	}
	task, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.RestoreTest, App: "hello", SecretRef: "restore-input"}, "operator", "restore")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionOperation(ctx, task.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteTask(ctx, task.ID, ops.Failed, ops.TaskOutcome{Error: "conflict"}); err != nil {
		t.Fatal(err)
	}
	plan := "sha256:" + strings.Repeat("a", 64)
	statements := []string{
		"INSERT INTO operations VALUES('cdn-op',NULL,'operator','cdn-key','queued','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','cdn','reserved','" + plan + "','')",
		"INSERT INTO cdn_plans VALUES('" + plan + "','reserved','reserved.example','attachment',X'7B7D')",
		"INSERT INTO cdn_sessions VALUES('cdn-op','" + plan + "',NULL,NULL)",
		"INSERT INTO cdn_reservations VALUES('reserved','reserved.example','attachment','cdn-op')",
	}
	for _, statement := range statements {
		if _, err = s.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	dropSQLitePreparationMigrationFixture(t, ctx, s)
	for _, statement := range []string{"UPDATE schema_version SET version=34", conflict} {
		if _, err = s.db.ExecContext(ctx, statement); statement != "" && err != nil {
			t.Fatal(statement, err)
		}
	}
	fingerprint := journalFingerprint(t, s.db)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return s.dir, fingerprint
}

func journalFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out string
	err := db.QueryRow(`SELECT
 (SELECT COALESCE(group_concat(x,'|'),'') FROM (SELECT id||'/'||COALESCE(plan_id,'-')||'/'||requester||'/'||idempotency_key||'/'||state||'/'||created_at||'/'||updated_at||'/'||kind||'/'||app||'/'||secret_ref||'/'||recovery_of AS x FROM operations ORDER BY id))
 ||'#'||(SELECT COALESCE(group_concat(x,'|'),'') FROM (SELECT operation_id||'/'||seq||'/'||kind||'/'||state||'/'||hex(payload)||'/'||created_at AS x FROM events ORDER BY operation_id,seq))
 ||'#'||(SELECT COALESCE(group_concat(x,'|'),'') FROM (SELECT operation_id||'/'||hex(canonical) AS x FROM operation_outcomes ORDER BY operation_id))
 ||'#'||(SELECT COALESCE(group_concat(x,'|'),'') FROM (SELECT app||'/'||operation_id AS x FROM cdn_reservations ORDER BY app))`).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSQLitePreparationMigrationPreservesV34Journal(t *testing.T) {
	dir, before := v34Store(t, "")
	ctx := context.Background()
	ro, err := OpenReadOnly(ctx, dir)
	var schemaErr *SchemaError
	if !errors.As(err, &schemaErr) || schemaErr.Version != 34 {
		if ro != nil {
			_ = ro.Close()
		}
		t.Fatalf("read-only open of old schema: %v", err)
	}
	s, err := OpenContext(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if after := journalFingerprint(t, s.db); after != before {
		t.Fatalf("migration changed journal records\nbefore %s\nafter  %s", before, after)
	}
	var triggers, indexes string
	if err = s.db.QueryRow("SELECT group_concat(name,',') FROM (SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='operations' ORDER BY name)").Scan(&triggers); err != nil || triggers != "cdn_reserved_app,legal_transition,operation_identity" {
		t.Fatalf("operations triggers: %q %v", triggers, err)
	}
	if err = s.db.QueryRow("SELECT group_concat(name,',') FROM (SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='operations' AND sql IS NOT NULL ORDER BY name)").Scan(&indexes); err != nil || indexes != "archive_maintenance_attempt,unresolved_source" {
		t.Fatalf("operations indexes: %q %v", indexes, err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO operations VALUES('blocked',NULL,'r','blocked','queued','t','t','sqlite_prepare','reserved',?,'')", strings.Repeat("b", 64)); err == nil || !strings.Contains(err.Error(), "cdn app reserved") {
		t.Fatalf("cdn reservation trigger lost: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE operations SET app='other' WHERE id='cdn-op'"); err == nil {
		t.Fatal("operation identity trigger lost")
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE operations SET state='succeeded' WHERE id='cdn-op'"); err == nil {
		t.Fatal("legal transition trigger lost")
	}
	assertSQLiteKindShapes(t, s)
}

func assertSQLiteKindShapes(t *testing.T, s *Store) {
	t.Helper()
	const insert = "INSERT INTO operations VALUES(?,?,'r',?,?,'t','t',?,?,?,?)"
	hash := strings.Repeat("c", 64)
	for name, row := range map[string][]any{
		"plan":    {"a", "sha256:" + hash, "k1", "queued", "sqlite_prepare", "hello", hash, ""},
		"short":   {"b", nil, "k2", "queued", "sqlite_prepare", "hello", "short", ""},
		"state":   {"c", nil, "k3", "preparing", "sqlite_prepare_abandon", "hello", hash, ""},
		"app":     {"d", nil, "k4", "queued", "sqlite_prepare", "", hash, ""},
		"recover": {"e", nil, "k5", "queued", "sqlite_prepare", "hello", hash, "x"},
	} {
		if _, err := s.db.Exec(insert, row...); err == nil {
			t.Fatalf("malformed %s SQLite task accepted", name)
		}
	}
	if _, err := s.db.Exec(insert, "ok", nil, "k6", "queued", "sqlite_prepare", "hello", hash, ""); err != nil {
		t.Fatalf("well-formed SQLite task refused: %v", err)
	}
}

func TestSQLitePreparationMigrationRollsBackOnFailure(t *testing.T) {
	dir, before := v34Store(t, "CREATE TABLE data_sqlite_task_inputs(conflict TEXT)")
	if s, err := OpenContext(t.Context(), dir); err == nil {
		_ = s.Close()
		t.Fatal("conflicting migration committed")
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version, prepared int
	if err = db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != 34 {
		t.Fatalf("version after rollback: %d %v", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='data_sqlite_preparations' OR name='operations_v35'").Scan(&prepared); err != nil || prepared != 0 {
		t.Fatalf("partial migration retained: %d %v", prepared, err)
	}
	if after := journalFingerprint(t, db); after != before {
		t.Fatal("rollback changed journal records")
	}
}
