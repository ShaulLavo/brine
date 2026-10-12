package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// ReadNativePreparedStopExpectation exposes immutable recorded expectations.
// Reopening still requires actual current host observation and fresh locks.
func (s *Store) ReadNativePreparedStopExpectation(ctx context.Context, pointID string) (NativePreparedPointView, NativeStopObservation, string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativePreparedPointView{}, NativeStopObservation{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	point, err := readNativePreparedPointTx(ctx, tx, pointID)
	if err != nil {
		return NativePreparedPointView{}, NativeStopObservation{}, "", err
	}
	intent, raw, err := readNativePreparationIntent(ctx, tx, pointID)
	if err != nil {
		return NativePreparedPointView{}, NativeStopObservation{}, "", err
	}
	events, err := readNativePreparationEvents(ctx, tx, intent, raw)
	var stopped NativeStopObservation
	if err != nil || len(events) != 6 || json.Unmarshal(events[1].Proof, &stopped) != nil || !validNativeStop(stopped, point.view.SourceKind) || nativePointHash(events[1].Proof) != point.view.StopProofSHA256 {
		return NativePreparedPointView{}, NativeStopObservation{}, "", &IntegrityError{}
	}
	var app string
	if err = tx.QueryRowContext(ctx, "SELECT app FROM operations WHERE id=?", point.view.PreparationOperationID).Scan(&app); err != nil {
		return NativePreparedPointView{}, NativeStopObservation{}, "", err
	}
	view := point.view
	view.PointSHA256 = point.hash
	return view, stopped, app, tx.Commit()
}
