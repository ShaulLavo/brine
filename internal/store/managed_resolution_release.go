package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
)

// CommitManagedResolutionRelease commits already applied create artifacts under
// their original immutable owner. A claimed resolver owns the new health/commit
// receipts; the source remains recovery_required and never gains a boot grant
// until that resolver reaches succeeded with the complete joined proof.
func (s *Store) CommitManagedResolutionRelease(ctx context.Context, resolver string, generation uint64, release Release) error {
	release, raw, err := normalizeRelease(release)
	if err != nil {
		return err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, d, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil {
		return err
	}
	if p.Kind != plan.Create || p.Lifecycle != "" || p.Managed == nil || verifyRelease(p.App, release, p, d) != nil || p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || *p.ObservedGeneration.Value > ^uint64(0)-2 || generation != *p.ObservedGeneration.Value+2 {
		return ErrConflict
	}
	var current uint64
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM releases)+(SELECT COUNT(*) FROM app_removals)+(SELECT COUNT(*) FROM data_archive_retirements)+(SELECT COUNT(*) FROM data_engine_preparations)+(SELECT COUNT(*) FROM data_engine_desired_states)+(SELECT COUNT(*) FROM data_engine_route_states)").Scan(&current); err != nil || current != generation {
		return ErrConflict
	}
	if err := managedResolutionCommitOwner(ctx, tx, resolver, release, p); err != nil {
		return err
	}
	var head string
	err = tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.App).Scan(&head)
	if !errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	}
	if p.Managed.Frontend != nil {
		if p.Image.ManifestDigest.Status != target.KnownStatus || p.Image.ManifestDigest.Value == nil {
			return ErrConflict
		}
		frontend, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
		if err != nil || frontend.Hash() != release.Managed.Frontend.Unit.Hash {
			return ErrConflict
		}
	}
	if err := admitManagedOwnedRelease(ctx, tx, p, release, true); err != nil {
		return err
	}
	return persistRelease(ctx, tx, p.App, release, raw)
}

func managedResolutionCommitOwner(ctx context.Context, tx *sql.Tx, resolver string, release Release, p plan.Plan) error {
	original, err := managedCommitOperation(ctx, tx, release.ID)
	if err != nil || original.State != ops.RecoveryRequired || original.Kind != ops.Deploy || original.PlanID != p.Hash || original.App != p.App {
		return ErrConflict
	}
	cursor, err := managedCreateCursor(ctx, tx, original.ID, p)
	if err != nil || cursor.Boundary.Step != "managed_route_check" && cursor.Boundary.Step != "managed_route_receipt" || cursor.FrontendStart.Step != "" {
		return ErrConflict
	}
	current := original
	for depth := 0; depth < 64; depth++ {
		child, exists, err := managedResolutionChild(ctx, tx, current.ID)
		if err != nil || !exists || child.Kind != ops.Resolve || child.RecoveryOf != current.ID || child.PlanID != p.Hash || child.App != p.App || child.SecretRef != original.SecretRef || managedResolutionReceipt(ctx, tx, child) != nil {
			return ErrConflict
		}
		last, err := managedCreateJournal(ctx, tx, child.ID, p)
		if err != nil {
			return err
		}
		if child.ID == resolver {
			if child.State != ops.Committing || last.Step != "managed_commit" || last.Outcome != "intent" && last.Outcome != "unknown" {
				return ErrConflict
			}
			return nil
		}
		if child.State != ops.RecoveryRequired {
			return ErrConflict
		}
		current = child
	}
	return ErrConflict
}
func managedCommitOperation(ctx context.Context, tx *sql.Tx, id string) (Operation, error) {
	return scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
}
func managedCreateJournal(ctx context.Context, tx *sql.Tx, id string, p plan.Plan) (ops.StepPayload, error) {
	cursor, err := managedCreateCursor(ctx, tx, id, p)
	return cursor.Boundary, err
}
func managedCreateCursor(ctx context.Context, tx *sql.Tx, id string, p plan.Plan) (ops.ManagedCreateCursor, error) {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM events WHERE operation_id=? AND kind='step' ORDER BY seq LIMIT 129", id)
	if err != nil {
		return ops.ManagedCreateCursor{}, err
	}
	defer func() { _ = rows.Close() }()
	events := []ops.Event{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return ops.ManagedCreateCursor{}, err
		}
		events = append(events, ops.Event{Kind: "step", Payload: raw})
	}
	if rows.Err() != nil || len(events) > 128 {
		return ops.ManagedCreateCursor{}, ErrConflict
	}
	last, valid := ops.ManagedCreateRecoveryCursor(events, p)
	if !valid {
		return last, ErrConflict
	}
	return last, nil
}
