package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) CDNByKey(ctx context.Context, requester, key string) (ops.Operation, error) {
	if requester == "" || key == "" || len(requester) > 256 || len(key) > 256 {
		return ops.Operation{}, ErrInvalid
	}
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM operations WHERE requester=? AND idempotency_key=?", requester, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ops.Operation{}, ErrNotFound
	}
	if err != nil {
		return ops.Operation{}, err
	}
	op, err := s.GetOperation(ctx, id)
	if err == nil && op.Kind != ops.CDN {
		return ops.Operation{}, ErrConflict
	}
	return op, err
}
