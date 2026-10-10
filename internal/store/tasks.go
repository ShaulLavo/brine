package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
)

// CompleteTask commits the bounded typed outcome, terminal state and state event
// atomically. A concurrent or restarted executor cannot overwrite the winner.
func (s *Store) CompleteTask(ctx context.Context, id string, state ops.State, outcome ops.TaskOutcome) error {
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil {
		return err
	}
	if op.State != ops.Preflight || !ops.CanTransitionFor(op.Kind, op.State, state) {
		return &ops.StateConflictError{Current: op.State}
	}
	op.State = state
	canonical, err := json.Marshal(outcome)
	if err != nil || len(canonical) > ops.MaxTaskOutcomeBytes || !outcome.Valid(op) {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operation_outcomes VALUES(?,?)", id, canonical); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE operations SET state=?,updated_at=? WHERE id=?", state, timestamp(), id); err != nil {
		return err
	}
	if _, err = appendEvent(ctx, tx, id, ops.Event{Kind: "state", State: state}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReadTaskOutcome(ctx context.Context, id string) (*ops.TaskOutcome, error) {
	op, err := s.GetOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	var canonical []byte
	err = s.db.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	outcome, err := ops.DecodeTaskOutcome(op, canonical)
	if err != nil {
		return nil, &IntegrityError{}
	}
	return &outcome, nil
}
