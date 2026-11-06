package store

import (
	"context"
	"errors"
	"time"
)

var errQuickCheck = errors.New("control store structural check unavailable")

// QuickCheck checks SQLite structure only. It does not validate retained authority.
// A caller that must leave live files unchanged must use OpenSnapshotReadOnly.
func (s *Store) QuickCheck(parent context.Context) (bool, error) {
	if err := parent.Err(); err != nil {
		return false, err
	}
	if !s.readOnly {
		return false, errQuickCheck
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "PRAGMA quick_check(1)")
	if err != nil {
		return false, quickCheckError(ctx)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return false, quickCheckError(ctx)
	}
	var value string
	if rows.Scan(&value) != nil {
		return false, quickCheckError(ctx)
	}
	healthy := value == "ok"
	if rows.Next() || rows.Err() != nil {
		return false, quickCheckError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return healthy, nil
}

func quickCheckError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errQuickCheck
}
