package store

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func publicationProofSchema27(t *testing.T) (string, *Store) {
	t.Helper()
	directory, db := publicationProofSchema26(t)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = migratePublicationSourcePreservationProof(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return directory, &Store{db: db, dir: directory}
}

func TestPublicationTransitionProofMigrationAdmitsActualWriterKindsAndRetainsRows(t *testing.T) {
	directory, legacy := publicationProofSchema27(t)
	raw := []byte{0, 'r', 'e', 't', 'a', 'i', 'n', 'e', 'd', '\n'}
	hash := publicationStorageSHA256(raw)
	for _, kind := range publicationProofKinds() {
		if kind == publicationTransitionTargetProof || kind == publicationTransitionCompletionProof {
			continue
		}
		if _, err := legacy.db.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('reviewed-plan',?,?,?,?)", publicationProofFixtureID(kind), kind, hash, raw); err != nil {
			t.Fatal("legacy row", kind, err)
		}
	}
	tx, err := legacy.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = insertPublicationProofTx(t.Context(), tx, "reviewed-plan", "target", publicationTransitionTargetProof, []byte("target")); err == nil {
		t.Fatal("schema27 unexpectedly admitted target writer")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if read, err := OpenReadOnly(t.Context(), directory); err == nil {
		_ = read.Close()
		t.Fatal("readonly opener upgraded schema27")
	}
	state, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	var version, count int
	if err = state.db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("schema upgrade", version, err)
	}
	if err = state.db.QueryRowContext(t.Context(), "SELECT count(*) FROM data_module_publication_proofs").Scan(&count); err != nil || count != len(publicationProofKinds())-2 {
		t.Fatal("retained row count", count, err)
	}
	for _, kind := range publicationProofKinds() {
		if kind == publicationTransitionTargetProof || kind == publicationTransitionCompletionProof {
			continue
		}
		tx, err = state.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		got, digest, readErr := readPublicationProofTx(t.Context(), tx, "reviewed-plan", publicationProofFixtureID(kind), string(kind))
		_ = tx.Rollback()
		if readErr != nil || digest != hash || !bytes.Equal(got, raw) {
			t.Fatal("retained row changed", kind, readErr)
		}
	}
	for _, kind := range []publicationProofKind{publicationTransitionTargetProof, publicationTransitionCompletionProof} {
		encoded, _ := json.Marshal(struct {
			Kind publicationProofKind `json:"kind"`
		}{kind})
		tx, err = state.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = insertPublicationProofTx(t.Context(), tx, "reviewed-plan", string(kind), kind, encoded); err != nil {
			_ = tx.Rollback()
			t.Fatal("production proof writer rejected", kind, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		tx, err = state.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		actual, digest, readErr := readPublicationProofTx(t.Context(), tx, "reviewed-plan", string(kind), string(kind))
		_ = tx.Rollback()
		if readErr != nil || !bytes.Equal(actual, encoded) || digest != publicationStorageSHA256(encoded) {
			t.Fatal("actual writer readback", kind, readErr)
		}
	}
	checkPublicationProofConstraints(t, state, hash, raw)
}

func TestPristinePublicationProofSchemaContainsCurrentKindDomain(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { //nolint:gosec // Private runner state directory fixture.
		t.Fatal(err)
	}
	state, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	var definition string
	if err := state.db.QueryRowContext(t.Context(), "SELECT sql FROM sqlite_master WHERE name='data_module_publication_proofs' AND type='table'").Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "kind IN ("+publicationProofKindConstraint()+")") {
		t.Fatal("pristine proof schema differs from writer domain")
	}
}

func publicationProofFixtureID(kind publicationProofKind) string {
	if kind == publicationPreflightProof {
		return "earned"
	}
	return string(kind)
}

func TestPublicationTransitionProofMigrationFailureRetainsSchemaAndBytes(t *testing.T) {
	_, legacy := publicationProofSchema27(t)
	raw := []byte("retained invalid foreign key fixture")
	hash := publicationStorageSHA256(raw)
	if _, err := legacy.db.ExecContext(t.Context(), "INSERT INTO data_module_publication_proofs VALUES('missing-claim','earned','preflight',?,?)", hash, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.migrate(t.Context()); err == nil {
		t.Fatal("schema28 migration accepted retained foreign key conflict")
	}
	var version, foreignKeys int
	if err := legacy.db.QueryRowContext(t.Context(), "SELECT version FROM schema_version").Scan(&version); err != nil || version != 27 {
		t.Fatal("failed upgrade changed schema", version, err)
	}
	if err := legacy.db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatal("failed upgrade lost foreign key enforcement", err)
	}
	var retained []byte
	if err := legacy.db.QueryRowContext(t.Context(), "SELECT canonical FROM data_module_publication_proofs WHERE id='earned'").Scan(&retained); err != nil || !bytes.Equal(retained, raw) {
		t.Fatal("failed upgrade changed proof", err)
	}
}
