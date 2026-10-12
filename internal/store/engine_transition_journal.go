package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

func readEngineTransition(ctx context.Context, q dataQuerier, id string) (data.EngineTransition, data.EngineTransitionCursor, error) {
	var input data.EngineTransition
	var cursor data.EngineTransitionCursor
	var raw []byte
	var operation, point string
	var binding data.ReplicaBindingID
	var fence data.FenceID
	err := q.QueryRowContext(ctx, "SELECT apply_operation_id,prepared_point_id,binding_id,fence_id,canonical FROM data_engine_transition_inputs WHERE id=?", id).Scan(&operation, &point, &binding, &fence, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return input, cursor, ErrNotFound
	}
	if err != nil {
		return input, cursor, err
	}
	input, err = data.DecodeEngineTransition(raw)
	common := input.Common()
	if err != nil || !bytes.Equal(raw, input.Bytes()) || common.Scope.ID != id || common.Scope.ApplyOperationID != operation ||
		common.PreparedPoint.ID != point || common.Scope.BindingID != binding || common.FenceID != fence {
		return input, cursor, &IntegrityError{}
	}
	cursor, err = readEngineTransitionEvents(ctx, q, input)
	return input, cursor, err
}

func readEngineTransitionEvents(ctx context.Context, q dataQuerier, input data.EngineTransition) (data.EngineTransitionCursor, error) {
	var cursor data.EngineTransitionCursor
	rows, err := q.QueryContext(ctx, "SELECT sequence,stage,canonical FROM data_engine_transition_events WHERE transition_id=? ORDER BY sequence", input.Common().Scope.ID)
	if err != nil {
		return cursor, err
	}
	defer func() { _ = rows.Close() }()
	var previous *data.EngineTransitionCursor
	for rows.Next() {
		var sequence uint64
		var stage data.EngineTransitionStage
		var raw []byte
		if err = rows.Scan(&sequence, &stage, &raw); err != nil {
			return cursor, err
		}
		event, err := data.DecodeEngineTransitionEvent(input, previous, raw)
		if err != nil || !bytes.Equal(raw, event.Bytes()) || event.Cursor().Sequence != sequence || event.Cursor().Stage != stage {
			return cursor, &IntegrityError{}
		}
		cursor = event.Cursor()
		previous = &cursor
	}
	if previous == nil {
		return cursor, &IntegrityError{}
	}
	return cursor, rows.Err()
}

func (s *Store) ReadEngineTransition(ctx context.Context, id string) (data.EngineTransition, data.EngineTransitionCursor, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return data.EngineTransition{}, data.EngineTransitionCursor{}, err
	}
	defer func() { _ = tx.Rollback() }()
	input, cursor, err := readEngineTransition(ctx, tx, id)
	if err != nil {
		return input, cursor, err
	}
	return input, cursor, tx.Commit()
}

func appendEngineTransitionEvent(ctx context.Context, tx *sql.Tx, input data.EngineTransition, old data.EngineTransitionCursor, event data.EngineTransitionEvent) error {
	current, cursor, err := readEngineTransition(ctx, tx, input.Common().Scope.ID)
	if err != nil || current.SHA256() != input.SHA256() || cursor != old {
		return errors.Join(ErrConflict, err)
	}
	checked, err := data.DecodeEngineTransitionEvent(input, &old, event.Bytes())
	if err != nil || checked.Cursor() != event.Cursor() {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_engine_transition_events VALUES(?,?,?,?)", old.ID, event.Cursor().Sequence, event.Cursor().Stage, event.Bytes())
	return err
}

func (s *Store) AdvanceEngineTransition(ctx context.Context, preparation *EnginePreparation, old data.EngineTransitionCursor, next data.EngineTransitionStage, observed data.EngineLayoutObservation) (data.EngineTransitionCursor, error) {
	input, _, err := s.ReadEngineTransition(ctx, old.ID)
	if err != nil || !s.transitionGuard(preparation, input) {
		return old, errors.Join(ErrConflict, err)
	}
	event, err := data.AdvanceEngineTransition(input, old, next, observed)
	if err != nil {
		return old, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return old, err
	}
	defer func() { _ = tx.Rollback() }()
	if err = checkEngineTransitionFence(ctx, tx, input); err != nil {
		return old, err
	}
	if err = checkTransitionRuntimeStage(ctx, tx, input, old); err != nil {
		return old, err
	}
	if err = appendEngineTransitionEvent(ctx, tx, input, old, event); err != nil {
		return old, err
	}
	return event.Cursor(), tx.Commit()
}

func checkTransitionRuntimeStage(ctx context.Context, q dataQuerier, input data.EngineTransition, cursor data.EngineTransitionCursor) error {
	if cursor.Stage != data.EngineTransitionActivated && cursor.Stage != data.EngineTransitionFinishIntent {
		return transitionHeadMatches(ctx, q, input)
	}
	head, _, err := readEngineHead(ctx, q, input.Common().Scope.BindingID)
	if err != nil || head.EpochID != input.Common().NewEpochID || head.RuntimeSHA256 != input.Common().TargetRuntimeSHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func (s *Store) transitionGuard(preparation *EnginePreparation, input data.EngineTransition) bool {
	return !s.readOnly && input.SHA256() != "" && preparation != nil && preparation.StateRoot() == s.dir && preparation.Active(input.Common().Scope.BindingID)
}

func checkEngineTransitionFence(ctx context.Context, q dataQuerier, input data.EngineTransition) error {
	c := input.Common()
	var held int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM data_fences f JOIN data_active_incarnations a ON a.incarnation_id=f.incarnation_id WHERE f.id=? AND f.operation_id=? AND f.database_id=? AND f.incarnation_id=? AND f.state='held'", c.FenceID, c.FenceOwnerOperationID, c.Scope.DatabaseID, c.Scope.IncarnationID).Scan(&held)
	if err != nil || held != 1 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func refuseNonterminalEngineFence(ctx context.Context, q dataQuerier, fence data.FenceID) error {
	rows, err := q.QueryContext(ctx, "SELECT id FROM data_engine_transition_inputs WHERE fence_id=?", fence)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		if err = engineFenceTerminal(ctx, q, id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func engineFenceTerminal(ctx context.Context, q dataQuerier, id string) error {
	_, cursor, err := readEngineTransition(ctx, q, id)
	if err != nil || cursor.Stage != data.EngineTransitionCompleted && cursor.Stage != data.EngineTransitionAbandoned {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
