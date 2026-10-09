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
func (s *Store) CreateOperation(ctx context.Context, intent ops.Intent, requester, idempotencyKey string) (Operation, bool, error) {
	if !ops.ValidIntent(intent) || len(requester) == 0 || len(requester) > 256 || len(idempotencyKey) == 0 || len(idempotencyKey) > 256 {
		return Operation{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	old, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref FROM operations WHERE requester=? AND idempotency_key=?", requester, idempotencyKey))
	if err == nil {
		if old.PlanID != intent.PlanID || old.Kind != intent.Kind || intent.Kind == ops.SecretSet && (old.App != intent.App || old.SecretRef != intent.SecretRef) {
			return Operation{}, false, ErrConflict
		}
		return old, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return Operation{}, false, err
	}
	app := intent.App
	var planID any
	if intent.Kind == ops.Deploy {
		p, _, e := loadPlan(ctx, tx, intent.PlanID)
		if e != nil {
			return Operation{}, false, e
		}
		app = p.App
		planID = intent.PlanID
	}
	id, err := newID()
	if err != nil {
		return Operation{}, false, err
	}
	now := timestamp()
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?,?)", id, planID, requester, idempotencyKey, ops.Queued, now, now, intent.Kind, app, intent.SecretRef); err != nil {
		return Operation{}, false, err
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref FROM operations WHERE id=?", id))
	if err != nil {
		return Operation{}, false, err
	}
	return op, false, tx.Commit()
}
func scanOperation(row interface{ Scan(...any) error }) (Operation, error) {
	var op Operation
	var created, updated string
	err := row.Scan(&op.ID, &op.PlanID, &op.Requester, &op.IdempotencyKey, &op.State, &created, &updated, &op.Kind, &op.App, &op.SecretRef)
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
	if err != nil || !ops.ValidOperation(op) {
		return Operation{}, &IntegrityError{}
	}
	return op, nil
}
func (s *Store) GetOperation(ctx context.Context, id OpID) (Operation, error) {
	return scanOperation(s.db.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref FROM operations WHERE id=?", id))
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
	var kind ops.Kind
	if err = tx.QueryRowContext(ctx, "SELECT state,kind FROM operations WHERE id=?", id).Scan(&from, &kind); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if expected != nil {
		if from != *expected || !ops.CanTransitionFor(kind, from, state) {
			return &ops.StateConflictError{Current: from}
		}
	} else if from == state {
		return tx.Commit()
	}
	if !ops.CanTransitionFor(kind, from, state) {
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
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref FROM operations WHERE id=?", id))
	if err != nil {
		return 0, err
	}
	if err = ops.ValidateOperationEvent(op, event); err != nil {
		return 0, err
	}
	var seq uint64
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE operation_id=?", id).Scan(&seq)
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
	op, err := s.GetOperation(ctx, id)
	if err != nil {
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
		if err != nil || ops.ValidateOperationEvent(op, e) != nil {
			return nil, &IntegrityError{}
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (s *Store) ListUnfinished(ctx context.Context) ([]Operation, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref FROM operations WHERE state NOT IN ('succeeded','failed','rolled_back','recovery_required') ORDER BY id")
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
	op, err := scanOperation(s.db.QueryRowContext(ctx, `SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref FROM operations WHERE app=? ORDER BY created_at DESC,id DESC LIMIT 1`, app))
	if err != nil {
		return Operation{}, err
	}
	if op.Kind == ops.Deploy {
		p, _, e := s.LoadPlan(ctx, op.PlanID)
		if e != nil {
			return Operation{}, e
		}
		if p.App != app {
			return Operation{}, &IntegrityError{}
		}
	}
	return op, nil
}
