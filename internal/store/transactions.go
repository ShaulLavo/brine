package store

import (
	"database/sql"
	"errors"
)

// rollbackOnExit releases an unfinished transaction and preserves cleanup
// failures. A prior Commit or Rollback legitimately leaves ErrTxDone.
func rollbackOnExit(tx *sql.Tx, resultErr *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*resultErr = errors.Join(*resultErr, err)
	}
}
