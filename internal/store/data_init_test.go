//go:build linux

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
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
	}, Journal: s, Requester: "operator", Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func(), error) { return func() {}, nil }, Quiesce: func(context.Context, datainit.Plan) (func(), error) { return func() {}, nil }, Facts: func(context.Context, datainit.Request) (datainit.Facts, error) {
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
	for _, statement := range []string{"UPDATE data_init_plans SET canonical='{}'", "DELETE FROM data_init_plans", "DELETE FROM data_init_operations", "DELETE FROM data_init_events"} {
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
