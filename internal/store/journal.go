package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
)

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%012x%s", time.Now().UnixMilli(), hex.EncodeToString(entropy[:])), nil
}
func (s *Store) CreateOperation(ctx context.Context, planID PlanID, requester, idempotencyKey string) (Operation, bool, error) {
	if len(requester) == 0 || len(requester) > 256 || len(idempotencyKey) == 0 || len(idempotencyKey) > 256 {
		return Operation{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	old, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,plan_id,requester,idempotency_key,state,created_at,updated_at FROM operations WHERE requester=? AND idempotency_key=?", requester, idempotencyKey))
	if err == nil {
		if old.PlanID != planID {
			return Operation{}, false, ErrConflict
		}
		return old, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return Operation{}, false, err
	}
	if _, _, err = loadPlan(ctx, tx, planID); err != nil {
		return Operation{}, false, err
	}
	id, err := newID()
	if err != nil {
		return Operation{}, false, err
	}
	now := timestamp()
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations VALUES(?,?,?,?,?,?,?)", id, planID, requester, idempotencyKey, ops.Queued, now, now); err != nil {
		return Operation{}, false, err
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,plan_id,requester,idempotency_key,state,created_at,updated_at FROM operations WHERE id=?", id))
	if err != nil {
		return Operation{}, false, err
	}
	return op, false, tx.Commit()
}
func scanOperation(row interface{ Scan(...any) error }) (Operation, error) {
	var op Operation
	var created, updated string
	err := row.Scan(&op.ID, &op.PlanID, &op.Requester, &op.IdempotencyKey, &op.State, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrNotFound
	}
	if err != nil {
		return op, err
	}
	op.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Operation{}, &IntegrityError{}
	}
	op.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil || !ops.ValidState(op.State) {
		return Operation{}, &IntegrityError{}
	}
	return op, nil
}
func (s *Store) GetOperation(ctx context.Context, id OpID) (Operation, error) {
	return scanOperation(s.db.QueryRowContext(ctx, "SELECT id,plan_id,requester,idempotency_key,state,created_at,updated_at FROM operations WHERE id=?", id))
}
func (s *Store) SetOperationState(ctx context.Context, id OpID, state State) error {
	return s.transitionOperation(ctx, id, nil, state)
}

// TransitionOperation is a compare-and-transition for competing lifecycle actors.
// A loser changes neither the operation nor its journal. It never retries using
// the newer state, since that could overwrite the executor's progress.
func (s *Store) TransitionOperation(ctx context.Context, id OpID, from, to ops.State) error {
	return s.transitionOperation(ctx, id, &from, to)
}

func (s *Store) transitionOperation(ctx context.Context, id OpID, expected *State, state State) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from State
	if err = tx.QueryRowContext(ctx, "SELECT state FROM operations WHERE id=?", id).Scan(&from); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if expected != nil {
		if from != *expected || !ops.CanTransition(from, state) {
			return &ops.StateConflictError{Current: from}
		}
	} else if from == state {
		return tx.Commit()
	}
	if !ops.CanTransition(from, state) {
		return &TransitionError{from, state}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE operations SET state=?,updated_at=? WHERE id=?", state, timestamp(), id); err != nil {
		return err
	}
	// State and its event commit together. There is no crash window with a changed
	// operation state and an absent state event.
	if _, err = appendEvent(ctx, tx, id, Event{Kind: "state", State: state}); err != nil {
		return err
	}
	return tx.Commit()
}
func appendEvent(ctx context.Context, tx *sql.Tx, id OpID, event Event) (uint64, error) {
	if err := ops.ValidateEvent(event); err != nil {
		return 0, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM operations WHERE id=?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, err
	}
	var seq uint64
	err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE operation_id=?", id).Scan(&seq)
	if err != nil {
		return 0, err
	}
	payload := []byte(event.Payload)
	if payload == nil {
		payload = []byte{}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO events VALUES(?,?,?,?,?,?)", id, seq, event.Kind, event.State, payload, timestamp())
	return seq, err
}
func (s *Store) AppendEvent(ctx context.Context, id OpID, event Event) (uint64, error) {
	if event.Kind == "state" {
		return 0, ops.ErrInvalidEvent
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	seq, err := appendEvent(ctx, tx, id, event)
	if err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}
func (s *Store) EventsAfter(ctx context.Context, id OpID, cursor uint64, limit int) ([]Event, error) {
	if limit < 1 || limit > 1024 || cursor > 1<<63-1 {
		return nil, ErrInvalid
	}
	if _, err := s.GetOperation(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT seq,kind,state,payload,created_at FROM events WHERE operation_id=? AND seq>? ORDER BY seq LIMIT ?", id, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		var created string
		if err = rows.Scan(&e.Sequence, &e.Kind, &e.State, &e.Payload, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil || ops.ValidateEvent(e) != nil {
			return nil, &IntegrityError{}
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (s *Store) ListUnfinished(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,plan_id,requester,idempotency_key,state,created_at,updated_at FROM operations WHERE state NOT IN ('succeeded','failed','rolled_back','recovery_required') ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Operation{}
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

// LastOperation includes failed and unfinished attempts, not just committed releases.
func (s *Store) LastOperation(ctx context.Context, app string) (Operation, error) {
	op, err := scanOperation(s.db.QueryRowContext(ctx, `SELECT o.id,o.plan_id,o.requester,o.idempotency_key,o.state,o.created_at,o.updated_at FROM operations o JOIN plans p ON p.id=o.plan_id WHERE json_extract(p.desired,'$.name')=? ORDER BY o.created_at DESC,o.id DESC LIMIT 1`, app))
	if err != nil {
		return Operation{}, err
	}
	p, _, err := s.LoadPlan(ctx, op.PlanID)
	if err != nil {
		return Operation{}, err
	}
	if p.App != app {
		return Operation{}, &IntegrityError{}
	}
	return op, nil
}
