package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
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
		timer := time.NewTimer(admissionJitter())
		select {
		case <-admission.Done():
			timer.Stop()
			cancel()
			return nil, func() {}, errors.Join(admission.Err(), err)
		case <-timer.C:
		}
	}
}

// admissionJitter spaces retried BEGINs 10-50ms apart. The repository's authority
// gate allows only crypto/rand in production code, and one byte is plenty here.
func admissionJitter() time.Duration {
	var b [1]byte
	_, _ = rand.Read(b[:])
	return time.Duration(10+int(b[0])%41) * time.Millisecond
}
