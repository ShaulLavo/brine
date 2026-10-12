//go:build linux

package store

import (
	"context"
	"testing"
	"time"
)

func TestArchiveCandidateReadOnlyUsesCommittedWALSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	writer := openTest(t)
	plan := storedArchivePlan(t, writer)
	reader, err := OpenReadOnly(ctx, writer.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	var journal string
	if err = writer.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatal("fixture is not using WAL", journal, err)
	}
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_writer_history SELECT id,? FROM data_databases WHERE incarnation_id=?", "pending-wal-write", plan.Candidate.Incarnation); err != nil {
		t.Fatal(err)
	}
	// The write transaction remains open during this read. Read-only readers must
	// see the earlier committed snapshot, not partial writes or a copied main DB.
	candidate, err := reader.ArchiveCandidate(ctx, plan.Candidate.App)
	if err != nil || len(candidate.Databases) != 1 || candidate.Databases[0].Written() {
		t.Fatal("uncommitted WAL read", candidate, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	candidate, err = reader.ArchiveCandidate(ctx, plan.Candidate.App)
	if err != nil || len(candidate.Databases) != 1 || !candidate.Databases[0].Written() {
		t.Fatal("committed WAL not observed", candidate, err)
	}
}
