package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
)

// FindSQLiteRetireOperation only recovers an exact durable acceptance. Fresh
// action availability and expiry do not invalidate a previously accepted task.
func (s *Store) FindSQLiteRetireOperation(ctx context.Context, in SQLiteRetireInput, requester, key string) (ops.Operation, bool, error) {
	if !in.valid() || requester == "" || len(requester) > 256 || key == "" || len(key) > 256 {
		return ops.Operation{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ops.Operation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE requester=? AND idempotency_key=?", requester, key))
	if errors.Is(err, ErrNotFound) {
		return ops.Operation{}, false, tx.Commit()
	}
	if err != nil {
		return ops.Operation{}, false, err
	}
	stored, err := readSQLiteRetireInput(ctx, tx, op)
	if err != nil {
		return ops.Operation{}, false, err
	}
	if stored != in || op.Requester != requester || op.IdempotencyKey != key {
		return ops.Operation{}, false, ErrConflict
	}
	return op, true, tx.Commit()
}
