package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
)

// managedResolutionSucceeded follows one bounded, unmixed resolution chain.
// The source operation remains immutable; success alone carries no boot grant.
func managedResolutionSucceeded(ctx context.Context, tx *sql.Tx, record ManagedFrontendRecord) (bool, error) {
	source, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", record.Resolution.Permit.OperationID))
	if err != nil {
		return false, err
	}
	if source.State != ops.RecoveryRequired || source.PlanID != record.Resolution.Permit.PlanID || source.App != record.Resolution.Permit.App || source.Kind != ops.Deploy && source.Kind != ops.Resolve {
		return false, nil
	}
	terminal, succeeded, err := managedResolutionTerminal(ctx, tx, source)
	if err != nil || !succeeded {
		return false, err
	}
	if err := managedResolutionBoundary(ctx, tx, terminal, "managed_commit"); err != nil {
		return false, err
	}
	return managedResolvedFrontendProof(ctx, tx, record, source)
}
func managedResolutionTerminal(ctx context.Context, tx *sql.Tx, source Operation) (Operation, bool, error) {
	current := source
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if seen[current.ID] {
			return Operation{}, false, ErrConflict
		}
		seen[current.ID] = true
		child, exists, err := managedResolutionChild(ctx, tx, current.ID)
		if err != nil {
			return Operation{}, false, err
		}
		if current.State == ops.Succeeded {
			if exists {
				return Operation{}, false, ErrConflict
			}
			return current, true, nil
		}
		if !exists || current.State != ops.RecoveryRequired || child.Kind != ops.Resolve || child.PlanID != source.PlanID || child.App != source.App || child.SecretRef != source.SecretRef || child.RecoveryOf != current.ID || child.State != ops.RecoveryRequired && child.State != ops.Succeeded {
			return Operation{}, false, nil
		}
		if err := managedResolutionReceipt(ctx, tx, child); err != nil {
			return Operation{}, false, err
		}
		current = child
	}
	return Operation{}, false, ErrConflict
}
func managedResolutionChild(ctx context.Context, tx *sql.Tx, source string) (Operation, bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM operations WHERE recovery_of=? ORDER BY id LIMIT 2", source)
	if err != nil {
		return Operation{}, false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return Operation{}, false, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Operation{}, false, err
	}
	if len(ids) == 0 {
		return Operation{}, false, nil
	}
	if len(ids) != 1 {
		return Operation{}, false, ErrConflict
	}
	child, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", ids[0]))
	return child, true, err
}
func managedResolutionReceipt(ctx context.Context, tx *sql.Tx, child Operation) error {
	var raw []byte
	var kind string
	if err := tx.QueryRowContext(ctx, "SELECT kind,payload FROM events WHERE operation_id=? ORDER BY seq LIMIT 1", child.ID).Scan(&kind, &raw); err != nil {
		return err
	}
	var receipt ops.ResolutionPayload
	if kind != "resolution" || ops.ValidateEvent(ops.Event{Kind: kind, Payload: raw}) != nil || json.Unmarshal(raw, &receipt) != nil || receipt.OperationID != child.RecoveryOf {
		return ErrConflict
	}
	return nil
}
func managedResolvedFrontendProof(ctx context.Context, tx *sql.Tx, record ManagedFrontendRecord, source Operation) (bool, error) {
	p, permit := record.Resolution.Plan, record.Resolution.Permit
	var releaseID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.App).Scan(&releaseID); err != nil {
		return false, err
	}
	release, err := readRelease(ctx, tx, p.App, releaseID)
	if err != nil {
		return false, err
	}
	if release.ID != source.ID || release.PlanID != p.Hash || release.Managed == nil || release.Managed.Frontend == nil || release.Managed.BindingID != permit.BindingID || release.Managed.EpochID != permit.EpochID || release.Managed.RuntimeSHA256 != permit.RuntimeSHA256 || release.Managed.Frontend.Unit.Hash != permit.UnitHash {
		return false, nil
	}
	desired, current, err := readEffectiveManagedDesired(ctx, tx, permit.BindingID, permit.EpochID, permit.RuntimeSHA256)
	if err != nil {
		return false, err
	}
	if !current || !desired.EngineRunning || !desired.FrontendRunning || !desired.IngressServing || desired.OperationID != source.ID || desired.PlanID != p.Hash {
		return false, nil
	}
	native, err := readEnginePermit(ctx, tx, permit.BindingID)
	raw, _ := json.Marshal(native.Runtime)
	if err != nil || digest(raw) != "sha256:"+permit.RuntimeSHA256 || !native.AllowsVerification(time.Now().UTC()) {
		return false, err
	}
	route, err := readManagedRouteState(ctx, tx, p.App)
	if err != nil {
		return false, err
	}
	return route.Present && route.BindingID == permit.BindingID && route.EpochID == permit.EpochID && route.RuntimeSHA256 == permit.RuntimeSHA256 && route.DesiredRevision == desired.Revision && route.PlanID == p.Hash && route.OperationID == source.ID && route.FileHash == release.CaddyFile.Hash && route.Generation == release.CaddyGeneration, nil
}

func managedResolutionBoundary(ctx context.Context, tx *sql.Tx, terminal Operation, boundary string) error {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM events WHERE operation_id=? AND kind='step' AND seq<(SELECT MIN(seq) FROM events WHERE operation_id=? AND kind='state' AND state='succeeded') ORDER BY seq LIMIT 129", terminal.ID, terminal.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var last ops.StepPayload
	count := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if ops.ValidateEvent(ops.Event{Kind: "step", Payload: raw}) != nil || json.Unmarshal(raw, &last) != nil {
			return ErrConflict
		}
		if managedRollbackStep(last.Step) {
			return ErrConflict
		}
		count++
		if count > 128 {
			return ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count == 0 || last.Step != boundary || last.Outcome != "completed" {
		return ErrConflict
	}
	return nil
}
func managedRollbackStep(step string) bool {
	return strings.HasPrefix(step, "rollback_") || step == "managed_restore_desired" || step == "managed_frontend_restore_permit" || step == "managed_restore_removed_frontend"
}
