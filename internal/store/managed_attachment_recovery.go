package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

func managedAttachmentRollbackOwner(ctx context.Context, tx *sql.Tx, resolver, owner string, p plan.Plan) error {
	current, err := managedCommitOperation(ctx, tx, resolver)
	if err != nil || current.Kind != ops.Resolve || current.State != ops.RollingBack {
		return ErrConflict
	}
	for depth := 0; depth < 64 && current.ID != owner; depth++ {
		if current.Kind != ops.Resolve || current.PlanID != p.Hash || current.App != p.App || managedResolutionReceipt(ctx, tx, current) != nil {
			return ErrConflict
		}
		parent, err := managedCommitOperation(ctx, tx, current.RecoveryOf)
		if err != nil || parent.State != ops.RecoveryRequired || parent.PlanID != p.Hash || parent.App != p.App || parent.SecretRef != current.SecretRef {
			return ErrConflict
		}
		child, exists, err := managedResolutionChild(ctx, tx, parent.ID)
		if err != nil || !exists || child.ID != current.ID {
			return ErrConflict
		}
		current = parent
	}
	if current.ID != owner || current.Kind != ops.Deploy {
		return ErrConflict
	}
	events, err := managedAttachmentSteps(ctx, tx, owner)
	if err != nil || !ops.InterruptedFrontendAttachment(events, p) {
		return ErrConflict
	}
	var releaseID string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", p.App).Scan(&releaseID); err != nil {
		return err
	}
	previous, err := readRelease(ctx, tx, p.App, releaseID)
	if err != nil || previous.Managed == nil || previous.Managed.Frontend != nil || previous.Managed.BindingID != p.Managed.Database.BindingID {
		return ErrConflict
	}
	return nil
}

func managedAttachmentSteps(ctx context.Context, tx *sql.Tx, owner string) ([]ops.Event, error) {
	rows, err := tx.QueryContext(ctx, "SELECT payload FROM events WHERE operation_id=? AND kind='step' ORDER BY seq LIMIT 129", owner)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := []ops.Event{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		events = append(events, ops.Event{Kind: "step", Payload: raw})
	}
	if rows.Err() != nil || len(events) > 128 {
		return nil, ErrConflict
	}
	return events, nil
}
