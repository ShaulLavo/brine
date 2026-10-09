package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
)

const removalSchema = `
CREATE TABLE app_removals (
 operation_id TEXT PRIMARY KEY REFERENCES operations(id),
 app TEXT NOT NULL, release_id TEXT NOT NULL, plan_id TEXT NOT NULL REFERENCES plans(id), committed_at TEXT NOT NULL,
 FOREIGN KEY(app,release_id) REFERENCES releases(app,id));
CREATE TRIGGER removals_no_update BEFORE UPDATE ON app_removals BEGIN SELECT RAISE(ABORT,'immutable app removal'); END;
CREATE TRIGGER removals_no_delete BEFORE DELETE ON app_removals BEGIN SELECT RAISE(ABORT,'immutable app removal'); END;
UPDATE schema_version SET version=3;
`

// RetireApp atomically releases the live head (and its port reservation) and
// records immutable removal evidence. Retained releases and D5 secrets survive.
func (s *Store) RetireApp(ctx context.Context, operationID, app, releaseID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldApp, oldRelease string
	err = tx.QueryRowContext(ctx, "SELECT app,release_id FROM app_removals WHERE operation_id=?", operationID).Scan(&oldApp, &oldRelease)
	if err == nil {
		if oldApp != app || oldRelease != releaseID {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var planID, opApp, kind string
	if err = tx.QueryRowContext(ctx, "SELECT plan_id,app,kind FROM operations WHERE id=?", operationID).Scan(&planID, &opApp, &kind); err != nil {
		return err
	}
	p, _, err := loadPlan(ctx, tx, planID)
	if err != nil {
		return err
	}
	if kind != "deploy" || opApp != app || p.App != app || p.Lifecycle != plan.RemoveApp || p.Removal == nil || p.Removal.ReleaseID != releaseID {
		return ErrInvalid
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM release_heads WHERE app=? AND current_id=?", app, releaseID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO app_removals VALUES(?,?,?,?,?)", operationID, app, releaseID, planID, timestamp()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AppRetired(ctx context.Context, operationID, app, releaseID string) (bool, error) {
	var a, r string
	err := s.db.QueryRowContext(ctx, "SELECT app,release_id FROM app_removals WHERE operation_id=?", operationID).Scan(&a, &r)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a != app || r != releaseID {
		return false, ErrConflict
	}
	return true, nil
}
