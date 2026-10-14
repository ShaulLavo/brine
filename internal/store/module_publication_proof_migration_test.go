package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const proofSchema26 = `
CREATE TABLE schema_version(version INTEGER NOT NULL); INSERT INTO schema_version VALUES(26);
CREATE TABLE operations(id TEXT PRIMARY KEY, plan_id TEXT, requester TEXT NOT NULL, idempotency_key TEXT NOT NULL, state TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, kind TEXT NOT NULL, app TEXT NOT NULL, secret_ref TEXT NOT NULL, recovery_of TEXT NOT NULL DEFAULT '', UNIQUE(requester,idempotency_key));
CREATE TABLE data_replica_bindings(id TEXT PRIMARY KEY);
CREATE TABLE data_native_task_inputs(id TEXT PRIMARY KEY,requester TEXT NOT NULL,kind TEXT NOT NULL,binding_id TEXT NOT NULL,canonical BLOB NOT NULL);
CREATE TABLE transitions(kind TEXT NOT NULL,from_state TEXT NOT NULL,to_state TEXT NOT NULL,PRIMARY KEY(kind,from_state,to_state));
CREATE TABLE data_module_publication_claims(plan_id TEXT PRIMARY KEY); INSERT INTO data_module_publication_claims VALUES('reviewed-plan');
CREATE TABLE data_module_publication_proofs(
 plan_id TEXT NOT NULL REFERENCES data_module_publication_claims(plan_id),id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('descriptor','staging_restore','preflight','publication_receipt','target_verification','target_checkpoint','target_material','recovery_release','retained_dependencies','staging_disposal','completion','guest_review','schema_requirements')),
 evidence_sha256 TEXT NOT NULL CHECK(length(evidence_sha256)=64),canonical BLOB NOT NULL CHECK(length(canonical)<=4194304),PRIMARY KEY(plan_id,id));
CREATE TRIGGER data_module_publication_proofs_no_update BEFORE UPDATE ON data_module_publication_proofs BEGIN SELECT RAISE(ABORT,'immutable module publication proof'); END;
CREATE TRIGGER data_module_publication_proofs_no_delete BEFORE DELETE ON data_module_publication_proofs BEGIN SELECT RAISE(ABORT,'retained module publication proof'); END;`

func publicationProofSchema26(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil { //nolint:gosec // Protected fixture state directory.
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600) //nolint:gosec // Fixed control.db member in the owned temporary fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.ExecContext(t.Context(), proofSchema26); err != nil {
		t.Fatal(err)
	}
	return dir, db
}

func TestPublicationProofMigrationPreservesBytesAndConstraints(t *testing.T) {
	dir, legacy := publicationProofSchema26(t)
	raw := []byte{0, 'f', 'i', 'x', 't', 'u', 'r', 'e', '\n'}
	hash := strings.Repeat("a", 64)
	if _, err := legacy.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('reviewed-plan','earned','preflight',?,?)", hash, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('reviewed-plan','preserved','source_preservation',?,?)", hash, raw); err == nil {
		t.Fatal("schema26 admitted new proof before migration")
	}
	if read, err := OpenReadOnly(t.Context(), dir); err == nil {
		_ = read.Close()
		t.Fatal("readonly opener migrated old schema")
	}
	state, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	var version, count int
	if err = state.db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("normal opener did not migrate", version, err)
	}
	var plan, id, kind, retainedHash string
	var retained []byte
	if err = state.db.QueryRowContext(t.Context(), "SELECT plan_id,id,kind,evidence_sha256,canonical FROM data_module_publication_proofs").Scan(&plan, &id, &kind, &retainedHash, &retained); err != nil || plan != "reviewed-plan" || id != "earned" || kind != "preflight" || retainedHash != hash || !bytes.Equal(retained, raw) {
		t.Fatal("earned proof bytes or identity changed", err)
	}
	if err = state.db.QueryRowContext(t.Context(), "SELECT count(*) FROM data_module_publication_proofs").Scan(&count); err != nil || count != 1 {
		t.Fatal("migration changed proof count", err)
	}
	if _, err = state.db.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('reviewed-plan','preserved','source_preservation',?,?)", hash, raw); err != nil {
		t.Fatal("valid preservation proof refused after normal migration", err)
	}
	checkPublicationProofConstraints(t, state, hash, raw)
	read, err := OpenReadOnly(t.Context(), dir)
	if err != nil {
		t.Fatal("readonly current schema refused migrated state", err)
	}
	defer func() { _ = read.Close() }()
	if _, err = read.db.ExecContext(t.Context(), "DELETE FROM data_module_publication_proofs"); err == nil {
		t.Fatal("readonly current schema became writable")
	}
}

func checkPublicationProofConstraints(t *testing.T, state *Store, hash string, raw []byte) {
	t.Helper()
	queries := []string{
		"INSERT INTO data_module_publication_proofs VALUES('reviewed-plan','bad','unknown_kind',?,?)",
		"INSERT INTO data_module_publication_proofs VALUES('foreign-plan','bad','source_preservation',?,?)",
		"INSERT INTO data_module_publication_proofs VALUES('reviewed-plan','earned','preflight',?,?)",
		"UPDATE data_module_publication_proofs SET evidence_sha256=? WHERE canonical=?",
		"DELETE FROM data_module_publication_proofs WHERE evidence_sha256=? AND canonical=?",
	}
	for _, query := range queries {
		if _, err := state.db.ExecContext(t.Context(), query, hash, raw); err == nil {
			t.Fatal("proof protection lost", query)
		}
	}
	for _, input := range []struct {
		hash string
		raw  []byte
	}{{hash: hash[:63], raw: raw}, {hash: hash, raw: make([]byte, (4<<20)+1)}} {
		if _, err := state.db.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('reviewed-plan','limit','source_preservation',?,?)", input.hash, input.raw); err == nil {
			t.Fatal("proof hash/byte limit lost")
		}
	}
}

func TestPublicationProofMigrationFailureRollsBackAndRestoresForeignKeys(t *testing.T) {
	dir, db := publicationProofSchema26(t)
	hash := strings.Repeat("a", 64)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('missing-claim','earned','preflight',?,?)", hash, []byte("untouched")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	state := &Store{db: db, dir: dir}
	if err := state.migrate(t.Context()); err == nil {
		t.Fatal("migration admitted invalid retained FK")
	}
	var version, foreignKeys int
	var raw []byte
	if err := db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 26 {
		t.Fatal("failed migration advanced schema", version, err)
	}
	if err := db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("failed migration lost FK enforcement", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT canonical FROM data_module_publication_proofs").Scan(&raw); err != nil || string(raw) != "untouched" {
		t.Fatal("failed migration changed retained bytes", err)
	}
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	if opened, err := OpenContext(parent, dir); !errors.Is(err, context.Canceled) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatal("canceled open lost cancellation", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 26 {
		t.Fatal("canceled opener changed schema", err)
	}
}
