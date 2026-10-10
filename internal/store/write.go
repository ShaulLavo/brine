package store

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"time"

	"modernc.org/sqlite"
)

const writeAdmissionLimit = 30 * time.Second

// Only a refused BEGIN is safe to retry: no transaction or effect exists yet.
// The admission deadline must not cancel a successfully admitted transaction.
func (s *Store) beginWrite(ctx context.Context) (*sql.Tx, context.CancelFunc, error) {
	admission, endAdmission := context.WithCancel(ctx)
	if _, deadline := ctx.Deadline(); !deadline {
		endAdmission()
		admission, endAdmission = context.WithTimeout(ctx, writeAdmissionLimit)
	}
	defer endAdmission()
	txCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(admission, cancel)
	defer stop()
	for {
		tx, err := s.db.BeginTx(txCtx, nil)
		if err == nil {
			if !stop() || admission.Err() != nil {
				tx.Rollback()
				cancel()
				return nil, func() {}, admission.Err()
			}
			return tx, cancel, nil
		}
		if admission.Err() != nil {
			cancel()
			return nil, func() {}, errors.Join(admission.Err(), err)
		}
		var busy *sqlite.Error
		if !errors.As(err, &busy) || busy.Code() != 5 {
			cancel()
			return nil, func() {}, err
		}
		timer := time.NewTimer(time.Duration(10+rand.IntN(41)) * time.Millisecond)
		select {
		case <-admission.Done():
			timer.Stop()
			cancel()
			return nil, func() {}, errors.Join(admission.Err(), err)
		case <-timer.C:
		}
	}
}
