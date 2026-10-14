//go:build linux

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

func claimManagedFrontendWake(t *testing.T, s *Store, id string) {
	t.Helper()
	for _, phase := range []ops.State{ops.Preflight, ops.Preparing, ops.Starting} {
		if err := s.SetOperationState(context.Background(), id, phase); err != nil {
			t.Fatal(err)
		}
	}
	appendManagedStep(t, s, id, "managed_recovery_frontend_start", "intent")
}
func TestManagedRecoveryFrontendPermissionBelongsOnlyToActiveClaim(t *testing.T) {
	s, p, source, resolver, _ := managedRouteCommitFixture(t)
	ctx := context.Background()
	record, err := s.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("unclaimed resolver granted start")
	}
	claimManagedFrontendWake(t, s, resolver.ID)
	pending, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || !pending.Pending || pending.PendingOperationID != resolver.ID || pending.Permit != record.Resolution.Permit || pending.Permit.OperationID != source.ID {
		t.Fatal("wake grant lost exact active owner", err, pending.PendingOperationID)
	}
	for _, outcome := range []string{"unknown", "completed"} {
		appendManagedStep(t, s, resolver.ID, "managed_recovery_frontend_start", outcome)
		if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err != nil {
			t.Fatal("active claimed wake lost its scoped permission", err)
		}
	}
	if err := s.SetOperationState(ctx, resolver.ID, ops.Checking); err != nil {
		t.Fatal(err)
	}
	appendManagedStep(t, s, resolver.ID, "managed_route_check", "completed")
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err != nil {
		t.Fatal("owned health check lost temporary permission", err)
	}
	if err := s.SetOperationState(ctx, resolver.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("terminal resolver retained start permission")
	}
	original, err := s.GetOperation(ctx, source.ID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("provisional permit changed source", err)
	}
}
func TestManagedRecoveryFrontendRefusesStaleAndForgedPermission(t *testing.T) {
	for _, name := range []string{"generation", "fence", "failed", "early-state", "foreign-plan", "unknown-earlier", "duplicate-start"} {
		t.Run(name, func(t *testing.T) {
			s, p, source, resolver, _ := managedRouteCommitFixture(t)
			ctx := context.Background()
			if name != "early-state" {
				claimManagedFrontendWake(t, s, resolver.ID)
			} else {
				appendManagedStep(t, s, resolver.ID, "managed_recovery_frontend_start", "intent")
			}
			switch name {
			case "generation":
				if _, err := s.db.ExecContext(ctx, "INSERT INTO data_engine_desired_states SELECT binding_id,revision+1,plan_id,operation_id,canonical FROM data_engine_desired_states WHERE binding_id=?", p.Managed.Database.BindingID); err != nil {
					t.Fatal(err)
				}
			case "fence":
				if _, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, "foreign-fence"); err != nil {
					t.Fatal(err)
				}
			case "failed":
				if err := s.SetOperationState(ctx, resolver.ID, ops.Failed); err != nil {
					t.Fatal(err)
				}
			case "foreign-plan":
				if _, err := s.db.ExecContext(ctx, "DROP TRIGGER operation_identity"); err != nil {
					t.Fatal(err)
				}
				var foreignPlan plan.Plan
				raw, _ := json.Marshal(p)
				if err := json.Unmarshal(raw, &foreignPlan); err != nil {
					t.Fatal(err)
				}
				foreignPlan.Hash = "sha256:" + strings.Repeat("f", 64)
				_, desired, err := s.LoadPlan(ctx, p.Hash)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.SavePlan(ctx, foreignPlan, desired); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, "UPDATE operations SET plan_id=? WHERE id=?", foreignPlan.Hash, resolver.ID); err != nil {
					t.Fatal(err)
				}
			case "unknown-earlier":
				if _, err := s.db.ExecContext(ctx, "DROP TRIGGER events_no_update"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, "UPDATE events SET payload=json_set(payload,'$.outcome','unknown') WHERE operation_id=? AND kind='step' AND json_extract(payload,'$.step')='frontend_start' AND json_extract(payload,'$.outcome')='completed'", source.ID); err != nil {
					t.Fatal(err)
				}
			case "duplicate-start":
				appendManagedStep(t, s, resolver.ID, "managed_recovery_frontend_start", "intent")
			}
			if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
				t.Fatal("stale or forged claim granted permission")
			}
		})
	}
}

func TestCommittedManagedReleaseKeepsOrdinaryPermitAndRejectsTemporaryWake(t *testing.T) {
	s, p, source, resolver := managedResolutionFixture(t)
	ctx := context.Background()
	record, err := s.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Queued, ops.Preflight, ops.Preparing, ops.Starting, ops.Checking, ops.Committing} {
		if state != ops.Queued {
			if err := s.SetOperationState(ctx, resolver.ID, state); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		_, temporary, err := managedRecoveryFrontend(ctx, tx, record)
		closeErr := tx.Rollback()
		if err != nil || closeErr != nil || temporary {
			t.Fatal("committed release reached temporary wake", state, err, closeErr, temporary)
		}
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("unfinished resolver bypassed joined ordinary permit")
	}
	if err := s.SetOperationState(ctx, resolver.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	granted, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || granted.Pending || granted.PendingOperationID != "" || granted.Permit != record.Resolution.Permit {
		t.Fatal("temporary inspector replaced committed serving proof", err)
	}
	unchanged, err := s.GetOperation(ctx, source.ID)
	if err != nil || unchanged.State != ops.RecoveryRequired {
		t.Fatal("committed serving proof changed original receipt", err)
	}
}
