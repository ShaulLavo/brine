//go:build linux

package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/restore"
)

func storeInitPlan(t *testing.T, s *Store) (datainit.Plan, ReservedDatabase) {
	t.Helper()
	ctx := context.Background()
	reserved, err := s.ReserveDatabase(ctx, dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	service := datainit.Service{PrepareRestorePoint: func(context.Context, datainit.Plan, datainit.Operation) (datainit.VerifiedRestorePoint, error) {
		return datainit.VerifiedRestorePoint{}, nil
	}, Journal: s, Requester: datainit.LocalOperatorRequester(), Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func(), error) { return func() {}, nil }, Quiesce: func(context.Context, datainit.Plan) (func(), error) { return func() {}, nil }, Facts: func(context.Context, datainit.Request) (datainit.Facts, error) {
		definition := data.SchemaDefinition{Database: reserved.Database.Name, Marker: "v1", CatalogSHA256: "688d95e9133c228079e32bcbdad7325064146b7b1be403a7bbe4a8b83a9c4134"}
		return datainit.Facts{Plan: datainit.Plan{ReplicaEpoch: reserved.Replica.EpochID, RemotePrefix: strings.TrimSuffix(reserved.Replica.RemotePrefix, "/"), Bounds: data.InitializationBounds{MaxBackupAgeSeconds: 300, MaxRestoreTestAgeSeconds: 300, RecoveryWindowSeconds: 3600}, PolicyHash: hash, TargetHash: hash, DesiredHash: hash, Database: reserved.Database, Definition: definition}, Initializer: data.SchemaInitializer{Definition: definition, Statements: []string{"CREATE TABLE t(x TEXT)"}}, Observation: data.SchemaObservation{State: data.AllocatedEmpty}}, nil
	}}
	p, err := service.Plan(ctx, datainit.Request{App: "example", FirstReleasePlan: hash, Artifact: hash})
	if err != nil {
		t.Fatal(err)
	}
	return p, reserved
}

func TestDataInitializationJournalAndFenceAreAtomic(t *testing.T) {
	s := openTest(t)
	p, reserved := storeInitPlan(t, s)
	ctx := context.Background()
	operation, err := s.ClaimInitialization(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID)
	if err != nil || permit.FenceState != "held" {
		t.Fatalf("claim did not fence: %+v %v", permit, err)
	}
	if _, err = s.ClaimInitialization(ctx, p); err == nil {
		t.Fatal("second attempt permitted")
	}
	for _, state := range []string{"quiesced", "restore_point_intent", "restore_point_verified", "mutation_intent", "mutation_completed", "succeeded"} {
		if state == "restore_point_verified" {
			if _, err = s.SetInitState(ctx, operation, "succeeded"); !errors.Is(err, ErrConflict) {
				t.Fatal("missing verification released fence", err)
			}
			point := storeInitPoint(p, operation)
			if err = s.SaveInitRestorePoint(ctx, operation, point); err != nil {
				t.Fatal(err)
			}
			if _, err = s.LoadInitRestorePoint(ctx, operation); err != nil {
				t.Fatal("verification receipt lost", err)
			}
			if _, err = s.ReadRestorePoint(ctx, p.RestorePointID, p.Database.ReplicaBindingID, p.ReplicaEpoch); err != nil {
				t.Fatal("shared restore point missing", err)
			}
		}
		operation, err = s.SetInitState(ctx, operation, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	permit, err = s.ReadReplicaPermit(ctx, reserved.Database.DatabaseID)
	if err != nil || permit.FenceState == "held" {
		t.Fatal("verified completion left fence held", err)
	}
	var consumed int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM data_writer_history WHERE database_id=?", reserved.Database.DatabaseID).Scan(&consumed); err != nil || consumed != 1 {
		t.Fatalf("empty allocation not consumed: %d %v", consumed, err)
	}
	read, exists, err := s.ReadInitOperation(ctx, p.ID)
	if err != nil || !exists || read.State != "succeeded" {
		t.Fatalf("journal result: %+v %v", read, err)
	}
	if _, err = s.SetInitState(ctx, operation, "mutation_intent"); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal replay allowed: %v", err)
	}
	for _, statement := range []string{"UPDATE data_init_plans SET canonical='{}'", "DELETE FROM data_init_plans", "DELETE FROM data_init_operations", "DELETE FROM data_init_events", "UPDATE data_restore_points SET canonical='{}'", "DELETE FROM data_restore_points"} {
		if _, err = s.db.Exec(statement); err == nil {
			t.Fatal("immutable journal changed")
		}
	}
}
func TestDataInitializationRefusesWriterHistoryAndForeignFence(t *testing.T) {
	for _, reason := range []string{"writer", "fence"} {
		t.Run(reason, func(t *testing.T) {
			s := openTest(t)
			p, reserved := storeInitPlan(t, s)
			ctx := context.Background()
			var err error
			if reason == "writer" {
				err = s.RecordWriterAttempt(ctx, reserved.Database.IncarnationID, "writer-attempt")
			} else {
				_, err = s.HoldDataFence(ctx, reserved.Database.DatabaseID, "foreign-operation")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ClaimInitialization(ctx, p); err == nil {
				t.Fatal("unsafe claim accepted")
			}
			if _, exists, err := s.ReadInitOperation(ctx, p.ID); err != nil || exists {
				t.Fatal("failed claim recorded an attempt")
			}
		})
	}
}

func storeInitPoint(p datainit.Plan, o datainit.Operation) datainit.VerifiedRestorePoint {
	now := time.Now().UTC()
	snapshot := restore.SnapshotSource{BindingID: string(p.Database.ReplicaBindingID), Epoch: string(p.ReplicaEpoch), PointID: p.RestorePointID, ObjectKey: p.RemotePrefix + "/restore-points/" + p.RestorePointID + "/snapshot.sqlite", SHA256: strings.Repeat("a", 64), Size: 4096}
	return datainit.VerifiedRestorePoint{PointID: p.RestorePointID, DatabaseID: p.Database.DatabaseID, UploadedAt: now, RetainUntil: now.Add(2 * time.Hour), Receipt: restore.Receipt{ToolVersion: restore.SnapshotToolVersion, OperationID: o.ID + "-empty-verify", Source: restore.RestoreSource{Kind: restore.SQLiteSnapshot, Snapshot: &snapshot}, ObservedAt: now, Schema: restore.SchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed"}}
}
func TestDataInitializationReceiptRemainsImmutableAndReopens(t *testing.T) {
	s := openTest(t)
	p, _ := storeInitPlan(t, s)
	ctx := context.Background()
	o, err := s.ClaimInitialization(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"quiesced", "restore_point_intent"} {
		o, err = s.SetInitState(ctx, o, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	point := storeInitPoint(p, o)
	changed := point
	changed.Receipt.OperationID = "foreign-proof"
	if err = s.SaveInitRestorePoint(ctx, o, changed); err == nil {
		t.Fatal("foreign proof accepted")
	}
	if err = s.SaveInitRestorePoint(ctx, o, point); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveInitRestorePoint(ctx, o, point); err == nil {
		t.Fatal("verification journal replayed")
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE data_init_events SET receipt='{}' WHERE receipt IS NOT NULL"); err == nil {
		t.Fatal("verification replaced")
	}
	read, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	got, err := read.LoadInitRestorePoint(ctx, o)
	if err != nil || got.Receipt.OperationID != point.Receipt.OperationID || got.RetainUntil != point.RetainUntil {
		t.Fatalf("receipt reopen %+v %v", got, err)
	}
}
