package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/plan"
)

func guardCDNReservation(ctx context.Context, tx *sql.Tx, app string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM cdn_reservations WHERE app=?", app).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}

func guardCDNPlan(ctx context.Context, tx *sql.Tx, p plan.Plan) error {
	head, err := readCDNRevision(ctx, tx, p.App, "")
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if head.Attachment.Mode != cdn.Enabled {
		return nil
	}
	if p.Lifecycle == plan.RemoveApp {
		return ErrConflict
	}
	_, desired, err := loadPlan(ctx, tx, p.Hash)
	if err != nil {
		return err
	}
	for _, domain := range desired.Domains {
		if string(domain) == head.Attachment.Binding.Hostname {
			return nil
		}
	}
	return ErrConflict
}
