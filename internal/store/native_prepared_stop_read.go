package store

import (
	"context"
	"encoding/json"
)

// ReadNativePreparedStop returns the retained original stop for comparison with
// fresh host inspection. It cannot reopen the source or authorize a mutation.
func (s *Store) ReadNativePreparedStop(ctx context.Context, pointID string) (NativeStopObservation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeStopObservation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil {
		return NativeStopObservation{}, err
	}
	intent, raw, err := readNativePreparationIntent(ctx, tx, pointID)
	if err != nil {
		return NativeStopObservation{}, err
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	if err != nil || len(events) != 6 {
		return NativeStopObservation{}, ErrConflict
	}
	var stopped NativeStopObservation
	if json.Unmarshal(events[1].Proof, &stopped) != nil || !validNativeStop(stopped, point.view.SourceKind) || nativePointHash(events[1].Proof) != point.view.StopProofSHA256 {
		return NativeStopObservation{}, &IntegrityError{}
	}
	return stopped, tx.Commit()
}
