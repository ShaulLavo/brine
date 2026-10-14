package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
)

// The existing frontend guard may honor one explicit wake intent from the
// unique active resolver, never the terminal source or a passive inspection.
func managedRecoveryFrontend(ctx context.Context, tx *sql.Tx, record ManagedFrontendRecord) (ManagedFrontendResolution, bool, error) {
	resolution := record.Resolution
	p := resolution.Plan
	if p.Kind != plan.Create || p.Lifecycle != "" || p.Managed == nil || p.Managed.Frontend == nil {
		return resolution, false, nil
	}
	source, err := managedCommitOperation(ctx, tx, resolution.Permit.OperationID)
	if err != nil || source.Kind != ops.Deploy || source.State != ops.RecoveryRequired || source.PlanID != p.Hash || source.App != p.App {
		return resolution, false, ErrConflict
	}
	cursor, err := managedCreateCursor(ctx, tx, source.ID, p)
	if err != nil || cursor.Boundary.Step != "managed_route_check" || cursor.FrontendStart.Step != "" {
		return resolution, false, err
	}
	resolver, applies, err := managedFrontendWakeOwner(ctx, tx, source, p)
	if err != nil || !applies {
		return resolution, false, err
	}
	if p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || *p.ObservedGeneration.Value > ^uint64(0)-2 {
		return resolution, true, ErrConflict
	}
	var generation uint64
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM releases)+(SELECT COUNT(*) FROM app_removals)+(SELECT COUNT(*) FROM data_archive_retirements)+(SELECT COUNT(*) FROM data_engine_preparations)+(SELECT COUNT(*) FROM data_engine_desired_states)+(SELECT COUNT(*) FROM data_engine_route_states)").Scan(&generation); err != nil || generation != *p.ObservedGeneration.Value+2 {
		return resolution, true, ErrConflict
	}
	var head string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.App).Scan(&head); !errors.Is(err, sql.ErrNoRows) {
		return resolution, true, ErrConflict
	}
	if _, err := managedOwnedControl(ctx, tx, p, source.ID); err != nil {
		return resolution, true, err
	}
	if p.Image.ManifestDigest.Status != target.KnownStatus || p.Image.ManifestDigest.Value == nil {
		return resolution, true, ErrConflict
	}
	unit, err := quadlet.Render(resolution.Desired, p, *p.Image.ManifestDigest.Value)
	if err != nil || unit.Hash() != resolution.Permit.UnitHash || resolution.Permit.BindingID != p.Managed.Database.BindingID || resolution.Permit.EpochID != p.Managed.Archive.EpochID || resolution.Permit.RuntimeSHA256 != p.Managed.RuntimeSHA256 {
		return resolution, true, ErrConflict
	}
	resolution.Pending = true
	resolution.PendingOperationID = resolver.ID
	return resolution, true, nil
}
func managedFrontendWakeOwner(ctx context.Context, tx *sql.Tx, source Operation, p plan.Plan) (Operation, bool, error) {
	current := source
	inheritedStart := false
	for depth := 0; depth < 64; depth++ {
		child, exists, err := managedResolutionChild(ctx, tx, current.ID)
		if err != nil || !exists {
			return Operation{}, false, err
		}
		if child.Kind != ops.Resolve || child.RecoveryOf != current.ID || child.PlanID != p.Hash || child.App != p.App || child.SecretRef != source.SecretRef || managedResolutionReceipt(ctx, tx, child) != nil {
			return Operation{}, false, ErrConflict
		}
		cursor, err := managedCreateCursor(ctx, tx, child.ID, p)
		if err != nil {
			return Operation{}, false, err
		}
		if child.State == ops.RecoveryRequired {
			inheritedStart = inheritedStart || cursor.FrontendStart.Step != ""
			current = child
			continue
		}
		if inheritedStart || child.State != ops.Starting && child.State != ops.Checking || cursor.FrontendStart.Step == "" || cursor.Boundary.Step != "managed_route_check" {
			return Operation{}, false, nil
		}
		_, descendant, err := managedResolutionChild(ctx, tx, child.ID)
		if err != nil || descendant {
			return Operation{}, false, ErrConflict
		}
		return child, true, nil
	}
	return Operation{}, false, ErrConflict
}
