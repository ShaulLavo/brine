//go:build linux

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restore"
)

func TestSQLiteStatusReadsLiveWALWithoutLogicalChanges(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(s.dir, "control.db")
	old, err := os.ReadFile(main) // #nosec G304 -- Exact test-created control database in a private fixture.
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := s.ReserveDatabase(t.Context(), dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(main) // #nosec G304 -- Exact test-created control database in a private fixture.
	if err != nil || !bytes.Equal(old, current) {
		t.Fatal("fixture is not WAL-only", err)
	}
	before := sqliteStatusLogicalDigest(t, s.db)
	reader, err := OpenReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	got, err := reader.ReadSQLiteStatus(t.Context(), "example", "")
	if err != nil || got.Replica.BindingID != reserved.Replica.BindingID || got.Facts.Committed || got.Facts.Credential.State != "missing" || got.Facts.LastVerified != nil {
		t.Fatalf("WAL facts missing: %+v %v", got, err)
	}
	if before != sqliteStatusLogicalDigest(t, s.db) {
		t.Fatal("status changed logical records or schema")
	}
	for _, path := range []string{s.dir, main} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("status changed private permissions", err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.dir, "credentials")); !os.IsNotExist(err) {
		t.Fatal("status created credential directories")
	}
}
func sqliteStatusLogicalDigest(t *testing.T, db *sql.DB) [32]byte {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	for _, name := range names {
		encoded = append(encoded, sqliteStatusTableBytes(t, db, name)...)
	}
	encoded = append(encoded, sqliteStatusTableBytes(t, db, "sqlite_master")...)
	return sha256.Sum256(encoded)
}
func sqliteStatusTableBytes(t *testing.T, db *sql.DB, name string) []byte {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`) // #nosec G202 -- Identifier comes from this controlled fixture's sqlite_master, with quotes escaped.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range targets {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, raw...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return encoded
}
func TestSQLiteStatusVerificationRequiresProtectedSuccessfulReceipt(t *testing.T) {
	s, b := committedDataFixture(t)
	now := time.Now().UTC()
	point := data.SQLiteRestorePoint{ID: strings.Repeat("d", 32), BindingID: b.BindingID, EpochID: b.EpochID, Kind: data.RestorePointLTX, Schema: data.SQLiteSchemaObservation{DatabaseID: b.DatabaseID, State: data.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256, ObservedAt: now}, RecordedAt: now, LTX: &data.RestoreLTXPoint{TXID: 7, Barrier: data.RestoreUploadBarrier{BindingID: b.BindingID, EpochID: b.EpochID, TXID: 7, ReplicaTXID: 7, ObservedAt: now}}}
	if err := s.SaveRestorePoint(t.Context(), point); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadSQLiteStatus(t.Context(), "example", "main")
	if err != nil || got.Facts.LastVerified != nil {
		t.Fatal("upload promoted to verified", err)
	}
	op, receipt := sqliteStatusCompletedRestore(t, s, b, "example", "main", 7)
	got, err = s.ReadSQLiteStatus(t.Context(), "example", "main")
	if err != nil || got.Facts.LastVerified == nil || got.Facts.LastVerified.OperationID != op.ID || !got.Facts.LastVerified.VerifiedAt.Equal(receipt.ObservedAt) {
		t.Fatalf("verified receipt lost: %+v %v", got, err)
	}
	// Newer remote uploads do not erase or refresh a historical verification.
	point.ID = strings.Repeat("e", 32)
	point.RecordedAt = now.Add(time.Minute)
	if err := s.SaveRestorePoint(t.Context(), point); err != nil {
		t.Fatal(err)
	}
	got, err = s.ReadSQLiteStatus(t.Context(), "example", "main")
	if err != nil || got.Facts.LastVerified.OperationID != op.ID {
		t.Fatal("upload displaced verified history", err)
	}
}
func sqliteStatusCompletedRestore(t *testing.T, s *Store, b data.ReplicaBinding, app, database string, txid uint64) (ops.Operation, restore.SQLiteReceipt) {
	t.Helper()
	input := ops.RestoreTaskInput{App: app, Database: database, TXID: fmt.Sprintf("%x", txid)}
	ref, err := s.SaveRestoreTaskInput(t.Context(), "operator", input)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.RestoreTest, App: app, SecretRef: ref}, "operator", fmt.Sprintf("verify-%s-%d", database, txid))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionOperation(t.Context(), op.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	r := restore.SQLiteReceipt{OperationID: op.ID, Source: restore.SQLiteSource{Kind: restore.LitestreamLTX, LTX: &restore.LTXSource{Recoverability: true, BindingID: string(b.BindingID), Epoch: string(b.EpochID), TXID: txid}}, ToolVersion: restore.LitestreamVersion, RequestedTXID: txid, RecoveredTXID: txid, ObservedAt: time.Now().UTC(), Schema: restore.SQLiteSchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, LossWindow: restore.LossWindow{State: restore.LossUnknown, Reason: "No independent last-commit coverage proof; asynchronous replication may lose recent writes."}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "unknown", PositionEvidence: "remote_dry_run_and_restore"}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(t.Context(), op.ID, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		t.Fatal(err)
	}
	return op, r
}
func TestSQLiteStatusRefusesCrossedVerificationAndCredentialScope(t *testing.T) {
	for _, kind := range []string{"database", "credential"} {
		t.Run(kind, func(t *testing.T) {
			sqliteStatusCrossedCase(t, kind)
		})
	}
}
func sqliteStatusCrossedCase(t *testing.T, kind string) {
	t.Helper()
	s, b := committedDataFixture(t)
	if kind == "database" {
		sqliteStatusCompletedRestore(t, s, b, "example", "other", 7)
	}
	if kind == "credential" {
		r := CredentialRecord{App: "foreign", ID: strings.Repeat("a", 32), Kind: "receipt", BindingID: b.BindingID, EpochID: b.EpochID, Destination: b.Destination.Reference, CredentialRef: b.Destination.CredentialRef, Version: 1, ReceivedAt: time.Now().UTC()}
		raw, _ := json.Marshal(r)
		if _, err := s.db.Exec("INSERT INTO data_credential_records VALUES(?,?,?,?)", r.ID, b.BindingID, "receipt", raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReadSQLiteStatus(t.Context(), "example", "main"); err == nil {
		t.Fatal("crossed protected facts admitted")
	}
}

func TestSQLiteStatusOldSchemaAndAbsentStoreRemainUnchanged(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("UPDATE schema_version SET version=33"); err != nil {
		t.Fatal(err)
	}
	before := sqliteStatusLogicalDigest(t, s.db)
	if reader, err := OpenReadOnly(context.Background(), s.dir); err == nil {
		_ = reader.Close()
		t.Fatal("old schema admitted")
	}
	if before != sqliteStatusLogicalDigest(t, s.db) {
		t.Fatal("old schema migrated")
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := OpenReadOnly(t.Context(), missing); err == nil {
		t.Fatal("missing store admitted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("created absent store")
	}
}

func TestSQLiteStatusIncludesOnlyIndependentlyVerifiedInitializationSnapshot(t *testing.T) {
	s := openTest(t)
	p, _ := storeInitPlan(t, s)
	job, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.DataInitApply, App: p.Request.App, SecretRef: p.ID}, p.Requester.String(), "status-init")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionOperation(t.Context(), job.ID, ops.Queued, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	op, err := s.ClaimInitialization(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"quiesced", "restore_point_intent"} {
		op, err = s.SetInitState(t.Context(), op, stage)
		if err != nil {
			t.Fatal(err)
		}
	}
	point := storeInitPoint(p, op)
	if err := s.SaveInitRestorePoint(t.Context(), op, point); err != nil {
		t.Fatal(err)
	}
	status, err := s.ReadSQLiteStatus(t.Context(), "example", "main")
	if err != nil || status.Facts.LastVerified == nil || status.Facts.LastVerified.Source.Snapshot == nil || status.Facts.LastVerified.Source.Snapshot.PointID != point.PointID || len(status.Facts.Fences) != 1 || status.Facts.Fences[0].OwnerOperation != op.ID {
		t.Fatalf("verified initialization history or original fence lost: %+v %v", status, err)
	}
}

func TestSQLiteStatusRefusesSnapshotWithoutSelectedPoint(t *testing.T) {
	raw, err := json.Marshal(ops.RestoreTaskInput{App: "example", Database: "main"})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(append([]byte("operator\x00"), raw...))
	op := ops.Operation{Requester: "operator", SecretRef: fmt.Sprintf("%x", digest)}
	receipt := restore.SQLiteReceipt{Source: restore.SQLiteSource{Kind: restore.SQLiteSnapshot, Snapshot: &restore.SnapshotSource{PointID: strings.Repeat("a", 32)}}}
	if sqliteStatusInput(raw, op, "example", "main", receipt) {
		t.Fatal("latest restore input was joined to an unselected snapshot")
	}
}
