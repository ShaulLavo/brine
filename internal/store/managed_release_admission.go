package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

func admitManagedRelease(ctx context.Context, tx *sql.Tx, p plan.Plan, r Release) error {
	return admitManagedOwnedRelease(ctx, tx, p, r, false)
}
func admitManagedOwnedRelease(ctx context.Context, tx *sql.Tx, p plan.Plan, r Release, recovered bool) error {
	var operationPlan, app string
	var state ops.State
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),app,state FROM operations WHERE id=?", r.ID).Scan(&operationPlan, &app, &state); err != nil || operationPlan != p.Hash || app != p.App || state != ops.Committing && !(recovered && state == ops.RecoveryRequired) {
		return ErrConflict
	}
	m := p.Managed
	if !managedAppOwns(ctx, tx, p.App, m.Database.IncarnationID) {
		return ErrConflict
	}
	desired, current, err := readEffectiveManagedDesired(ctx, tx, m.Database.BindingID, m.Archive.EpochID, m.RuntimeSHA256)
	if err != nil || !current || !desired.EngineRunning || !desired.IngressServing || desired.FrontendRunning != (m.Frontend != nil) || desired.PlanID != p.Hash || desired.OperationID != r.ID {
		return ErrConflict
	}
	permit, err := readEnginePermit(ctx, tx, m.Database.BindingID)
	raw, _ := json.Marshal(permit.Runtime)
	if err != nil || digest(raw) != "sha256:"+m.RuntimeSHA256 || !permit.AllowsVerification(time.Now().UTC()) {
		return ErrConflict
	}
	route, err := readManagedRouteState(ctx, tx, p.App)
	if err != nil || route.BindingID != m.Database.BindingID || route.EpochID != m.Archive.EpochID || route.RuntimeSHA256 != m.RuntimeSHA256 || route.DesiredRevision != desired.Revision || route.PlanID != p.Hash || route.OperationID != r.ID || !route.Present || route.FileHash != r.CaddyFile.Hash || route.Generation != r.CaddyGeneration {
		return ErrConflict
	}
	if m.Frontend != nil {
		record, err := readManagedFrontendRecord(ctx, tx, p.App)
		frontend := record.Resolution
		if !recovered {
			frontend, err = readManagedFrontendPermit(ctx, tx, p.App)
		}
		if err != nil || frontend.Permit.OperationID != r.ID || frontend.Permit.PlanID != p.Hash || frontend.Permit.UnitHash != r.Managed.Frontend.Unit.Hash {
			return ErrConflict
		}
		return nil
	}
	// Removing this mutable head withdraws boot authority atomically with the new
	// engine-only serving release. Immutable frontend permits remain as evidence.
	_, err = tx.ExecContext(ctx, "DELETE FROM data_engine_frontend_heads WHERE app=?", p.App)
	return err
}
