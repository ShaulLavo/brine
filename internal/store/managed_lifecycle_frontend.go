package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

// A fresh reviewed start uses existing owned frontend artifacts and current
// lifecycle intent/route receipts. It never extends an older recovery proof.
func managedLifecycleFrontend(ctx context.Context, tx *sql.Tx, record ManagedFrontendRecord) (ManagedFrontendResolution, bool, error) {
	var none ManagedFrontendResolution
	native, err := readEnginePermit(ctx, tx, record.Resolution.Permit.BindingID)
	if err != nil {
		return none, false, err
	}
	raw, _ := json.Marshal(native.Runtime)
	runtimeHash := strings.TrimPrefix(digest(raw), "sha256:")
	desired, current, err := readEffectiveManagedDesired(ctx, tx, native.Runtime.BindingID, native.Runtime.EpochID, runtimeHash)
	if err != nil {
		return none, false, err
	}
	if !current || !desired.EngineRunning || !desired.FrontendRunning || !desired.IngressServing {
		return none, false, nil
	}
	head := desired
	desired, restored, err := managedLifecycleIntent(ctx, tx, head)
	if err != nil {
		return none, false, err
	}
	p, d, err := loadPlan(ctx, tx, desired.PlanID)
	if err != nil {
		return none, false, err
	}
	if p.Lifecycle != plan.StartApp && p.Lifecycle != plan.RestartApp {
		return none, false, nil
	}
	if p.Managed == nil || p.ManagedLifecycle == nil || !p.ManagedLifecycle.Valid(p.Lifecycle, *p.Managed, p.ManagedIntentRevision) || !p.ManagedLifecycle.Frontend || p.App != record.Resolution.Permit.App || p.Managed.Database.BindingID != desired.BindingID || p.Managed.Archive.EpochID != desired.EpochID || p.Managed.RuntimeSHA256 != desired.RuntimeSHA256 || d.SpacetimeDB == nil || !slices.Contains(d.SpacetimeDB.Module.Accepts, native.Runtime.Module.Schema.Marker) || !native.AllowsVerification(time.Now().UTC()) {
		return none, false, ErrConflict
	}
	var releaseID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.App).Scan(&releaseID); err != nil {
		return none, false, err
	}
	release, err := readRelease(ctx, tx, p.App, releaseID)
	if err != nil {
		return none, false, err
	}
	frontend := p.Managed.Frontend
	if release.PlanID != record.Resolution.Permit.PlanID || release.Managed == nil || release.Managed.Frontend == nil || release.Managed.BindingID != desired.BindingID || release.Managed.Frontend.Unit.Hash != record.Resolution.Permit.UnitHash || !reflect.DeepEqual(release.Managed.Frontend.Image, frontend.Image) || release.Managed.Frontend.Port != frontend.Port || !reflect.DeepEqual(release.Managed.Frontend.Secrets, frontend.Secrets) {
		return none, false, ErrConflict
	}
	var operationPlan, app string
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),app,state FROM operations WHERE id=?", desired.OperationID).Scan(&operationPlan, &app, &state); err != nil {
		return none, false, err
	}
	if operationPlan != p.Hash || app != p.App {
		return none, false, ErrConflict
	}
	view := ManagedFrontendResolution{Plan: p, Desired: d, Permit: ManagedFrontendPermit{App: p.App, OperationID: desired.OperationID, PlanID: p.Hash, BindingID: desired.BindingID, EpochID: desired.EpochID, RuntimeSHA256: desired.RuntimeSHA256, UnitHash: record.Resolution.Permit.UnitHash}}
	if state == ops.Starting || state == ops.Checking || state == ops.Committing {
		view.Pending = true
		return view, true, nil
	}
	proofOperation := desired.OperationID
	if state == ops.RecoveryRequired {
		source, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", desired.OperationID))
		if err != nil || source.Kind != ops.Deploy && source.Kind != ops.Resolve {
			return none, false, ErrConflict
		}
		terminal, completed, err := managedResolutionTerminal(ctx, tx, source)
		if err != nil || !completed {
			return none, false, ErrConflict
		}
		if err := managedResolutionBoundary(ctx, tx, terminal, "managed_route_receipt"); err != nil {
			return none, false, err
		}
		proofOperation, state = terminal.ID, ops.Succeeded
	}
	if state != ops.Succeeded {
		return none, false, ErrConflict
	}
	if err := managedLifecycleCompleted(ctx, tx, proofOperation); err != nil {
		return none, false, err
	}
	route, err := readManagedRouteState(ctx, tx, p.App)
	if err != nil {
		return none, false, err
	}
	receipt := desired
	if restored && route.OperationID == head.OperationID {
		receipt = head
	}
	if !route.Present || route.BindingID != receipt.BindingID || route.EpochID != receipt.EpochID || route.RuntimeSHA256 != receipt.RuntimeSHA256 || route.DesiredRevision != receipt.Revision || route.PlanID != receipt.PlanID || route.OperationID != receipt.OperationID {
		return none, false, ErrConflict
	}
	return view, true, nil
}
func managedLifecycleCompleted(ctx context.Context, tx *sql.Tx, operation string) error {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM events WHERE operation_id=? AND kind='step' AND seq<(SELECT MIN(seq) FROM events WHERE operation_id=? AND kind='state' AND state='succeeded') ORDER BY seq LIMIT 129", operation, operation)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	outcomes := map[string]string{}
	count := 0
	for rows.Next() {
		var raw []byte
		var step ops.StepPayload
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		if ops.ValidateEvent(ops.Event{Kind: "step", Payload: raw}) != nil || json.Unmarshal(raw, &step) != nil || managedRollbackStep(step.Step) {
			return ErrConflict
		}
		outcomes[step.Step] = step.Outcome
		count++
		if count > 128 {
			return ErrConflict
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, step := range []string{"managed_resume", "managed_frontend_start", "managed_frontend_check", "managed_route", "managed_route_receipt"} {
		if outcomes[step] != "completed" {
			return errors.Join(ErrConflict, errors.New("managed lifecycle observations incomplete"))
		}
	}
	return nil
}
