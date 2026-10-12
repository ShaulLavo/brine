package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

type nativeLiveRestoreAttempt struct {
	plan    liverestore.Plan
	claim   liverestore.Event
	current liverestore.Event
}

func readNativeLiveRestoreAttempt(ctx context.Context, q dataQuerier, operationID string) (nativeLiveRestoreAttempt, error) {
	var retained nativeLiveRestoreAttempt
	var planID, pointID string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT plan_id,prepared_point_id,binding_id,incoming_epoch_id,canonical FROM data_native_live_restore_claims WHERE operation_id=?", operationID).Scan(&planID, &pointID, &binding, &epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return retained, ErrNotFound
	}
	if err != nil {
		return retained, err
	}
	retained.plan, err = readNativeLiveRestorePlanTx(ctx, q, planID)
	if err != nil {
		return retained, err
	}
	retained.claim, err = liverestore.DecodeEvent(retained.plan, nil, raw)
	attempt := retained.claim.Attempt()
	if err != nil || attempt.OperationID != operationID || attempt.PlanID != planID || attempt.PreparedPointID != pointID || attempt.BindingID != binding || attempt.IncomingEpochID != epoch {
		return retained, &IntegrityError{}
	}
	retained.current, err = readNativeLiveRestoreEvents(ctx, q, retained)
	return retained, err
}

func readNativeLiveRestoreEvents(ctx context.Context, q dataQuerier, retained nativeLiveRestoreAttempt) (liverestore.Event, error) {
	rows, err := q.QueryContext(ctx, "SELECT sequence,stage,canonical FROM data_native_live_restore_events WHERE operation_id=? ORDER BY sequence", retained.claim.Attempt().OperationID)
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = rows.Close() }()
	var previous *liverestore.Event
	for rows.Next() {
		var sequence uint64
		var stage liverestore.Stage
		var raw []byte
		if err = rows.Scan(&sequence, &stage, &raw); err != nil {
			return liverestore.Event{}, err
		}
		event, err := liverestore.DecodeEvent(retained.plan, previous, raw)
		if err != nil || event.Sequence() != sequence || event.Stage() != stage || previous == nil && !bytes.Equal(event.Bytes(), retained.claim.Bytes()) {
			return liverestore.Event{}, &IntegrityError{}
		}
		previous = &event
	}
	if err = rows.Err(); err != nil {
		return liverestore.Event{}, err
	}
	if previous == nil {
		return liverestore.Event{}, &IntegrityError{}
	}
	return *previous, nil
}

// appendNativeLiveRestoreEventTx is the journal storage half. Its fixed caller
// must authenticate an actual protected20 proof before settling an intent. This
// private helper never performs effects or provides point-consumer authority.
func (s *Store) appendNativeLiveRestoreEventTx(ctx context.Context, tx *sql.Tx, preparation *EnginePreparation, old, next liverestore.Event) error {
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, old.Attempt().OperationID)
	if err != nil {
		return err
	}
	if preparation == nil || preparation.root != s.dir || !preparation.Active(retained.claim.Attempt().BindingID) ||
		retained.current.SHA256() != old.SHA256() || retained.current.Sequence() != old.Sequence() {
		return ErrConflict
	}
	decoded, err := liverestore.DecodeEvent(retained.plan, &retained.current, next.Bytes())
	if err != nil || decoded.SHA256() != next.SHA256() {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_events VALUES(?,?,?,?)", old.Attempt().OperationID, next.Sequence(), next.Stage(), next.Bytes())
	return err
}

func (s *Store) ReadNativeLiveRestoreProgress(ctx context.Context, operationID string) (liverestore.Event, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return liverestore.Event{}, err
	}
	defer func() { _ = tx.Rollback() }()
	retained, err := readNativeLiveRestoreAttempt(ctx, tx, operationID)
	if err != nil {
		return liverestore.Event{}, err
	}
	return retained.current, tx.Commit()
}
