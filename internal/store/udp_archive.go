package store

import (
	"context"
	"database/sql"
)

// CheckArchiveUDP is only retirement admission. Shared archive-candidate and
// writer-schema reads remain available for ordinary deployment and abandonment.
func (s *Store) CheckArchiveUDP(ctx context.Context, app string) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := guardArchiveUDP(ctx, tx, app); err != nil {
		return err
	}
	return tx.Commit()
}
func guardArchiveUDP(ctx context.Context, tx *sql.Tx, app string) error {
	rows, err := readUDPRows(ctx, tx, app)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State != "released" {
			return ErrConflict
		}
	}
	return nil
}
