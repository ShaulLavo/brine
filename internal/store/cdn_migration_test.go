package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func dropCDNMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER cdn_reserved_app"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cdn_reservations", "cdn_effects", "cdn_evidence", "cdn_heads", "cdn_revisions", "cdn_sessions", "cdn_plans"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCDNMigrationPreservesPriorOperationAndJournal(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(32);
CREATE TABLE plans(id TEXT PRIMARY KEY);
CREATE TABLE transitions(kind TEXT NOT NULL,from_state TEXT NOT NULL,to_state TEXT NOT NULL);
CREATE TABLE operations(id TEXT PRIMARY KEY,plan_id TEXT,requester TEXT NOT NULL,idempotency_key TEXT NOT NULL,state TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,kind TEXT NOT NULL,app TEXT NOT NULL,secret_ref TEXT NOT NULL,recovery_of TEXT NOT NULL);
CREATE TABLE events(operation_id TEXT NOT NULL,seq INTEGER NOT NULL,kind TEXT NOT NULL,state TEXT NOT NULL,payload BLOB NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(operation_id,seq));
INSERT INTO operations VALUES('old',NULL,'operator','old-key','preparing','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','secret_set','hello','asset','');
INSERT INTO events VALUES('old',1,'state','preparing',X'','2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = migrateCDNAttachments(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var state, kind string
	if err = db.QueryRowContext(ctx, "SELECT state,kind FROM operations WHERE id='old'").Scan(&state, &kind); err != nil || state != "preparing" || kind != "secret_set" {
		t.Fatal("prior operation changed", state, kind, err)
	}
	var payload []byte
	if err = db.QueryRowContext(ctx, "SELECT payload FROM events WHERE operation_id='old' AND seq=1").Scan(&payload); err != nil || len(payload) != 0 {
		t.Fatal("prior journal changed", err)
	}
	if _, err = db.ExecContext(ctx, "UPDATE operations SET secret_ref='other' WHERE id='old'"); err == nil {
		t.Fatal("migration lost immutable operation identity")
	}
	if _, err = db.ExecContext(ctx, "UPDATE operations SET state='succeeded' WHERE id='old'"); err == nil {
		t.Fatal("migration lost transition enforcement")
	}
	var version int
	if err = db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil || version != 33 {
		t.Fatal(version, err)
	}
}
