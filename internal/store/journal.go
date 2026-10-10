package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return Operation{}, false, err
	}
	defer tx.Rollback()
	old, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE requester=? AND idempotency_key=?", requester, idempotencyKey))
	if err == nil {
		if old.PlanID != intent.PlanID || old.Kind != intent.Kind || old.RecoveryOf != intent.RecoveryOf || (intent.Kind == ops.SecretSet || intent.Kind == ops.Resolve && intent.PlanID == "") && (old.App != intent.App || old.SecretRef != intent.SecretRef) {
			return Operation{}, false, ErrConflict
		}
		return old, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return Operation{}, false, err
	}
	app := intent.App
	var planID any
	if intent.Kind == ops.Deploy || intent.Kind == ops.Resolve && intent.PlanID != "" {
		p, _, e := loadPlan(ctx, tx, intent.PlanID)
		if e != nil {
			return Operation{}, false, e
		}
		app = p.App
		planID = intent.PlanID
	}
	if intent.Kind == ops.Resolve {
		source, e := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", intent.RecoveryOf))
		if e != nil {
			return Operation{}, false, e
		}
		if source.State != ops.RecoveryRequired || source.PlanID != intent.PlanID || source.App != app || source.SecretRef != intent.SecretRef || source.Kind != ops.Deploy && source.Kind != ops.Resolve && source.Kind != ops.SecretSet {
			return Operation{}, false, ErrConflict
		}
		if e = resolutionFamilyAvailable(ctx, tx, source); e != nil {
			return Operation{}, false, e
		}
	}
	id, err := newID()
	if err != nil {
		return Operation{}, false, err
	}
	now := timestamp()
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?,?,?)", id, planID, requester, idempotencyKey, ops.Queued, now, now, intent.Kind, app, intent.SecretRef, intent.RecoveryOf); err != nil {
		return Operation{}, false, err
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil {
		return Operation{}, false, err
	}
	if intent.Kind == ops.Resolve {
		payload, _ := json.Marshal(ops.ResolutionPayload{OperationID: intent.RecoveryOf})
		if _, err = appendEvent(ctx, tx, id, Event{Kind: "resolution", Payload: payload}); err != nil {
			return Operation{}, false, err
		}
		rows, e := tx.QueryContext(ctx, "SELECT kind,state,payload FROM events WHERE operation_id=? AND kind IN ('step','secret_version') ORDER BY seq", intent.RecoveryOf)
		if e != nil {
			return Operation{}, false, e
		}
		var adopted []Event
		for rows.Next() {
			var event Event
			if e = rows.Scan(&event.Kind, &event.State, &event.Payload); e != nil {
				rows.Close()
				return Operation{}, false, e
			}
			adopted = append(adopted, event)
			if len(adopted) > 4096 {
				rows.Close()
				return Operation{}, false, ErrInvalid
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return Operation{}, false, e
		}
		for _, event := range adopted {
			if _, e = appendEvent(ctx, tx, id, event); e != nil {
				return Operation{}, false, e
			}
		}
	}
	return op, false, tx.Commit()
}
func scanOperation(row interface{ Scan(...any) error }) (Operation, error) {
	var op Operation
	var created, updated string
	err := row.Scan(&op.ID, &op.PlanID, &op.Requester, &op.IdempotencyKey, &op.State, &created, &updated, &op.Kind, &op.App, &op.SecretRef, &op.RecoveryOf)
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
	return scanOperation(s.db.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
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
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
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
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
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
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
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
	rows, err := s.db.QueryContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE state NOT IN ('succeeded','failed','rolled_back','recovery_required') ORDER BY id")
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
	op, err := scanOperation(s.db.QueryRowContext(ctx, `SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE app=? ORDER BY created_at DESC,id DESC LIMIT 1`, app))
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

// CreateReconcileOperation records a standalone recovery request without inventing
// a deployment plan. Its ID is polled through the normal operation journal.
func (s *Store) CreateReconcileOperation(ctx context.Context, requester string) (Operation, error) {
	if requester == "" || len(requester) > 256 {
		return Operation{}, ErrInvalid
	}
	key, err := newID()
	if err != nil {
		return Operation{}, err
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Reconcile}, requester, "reconcile-"+key)
	return op, err
}
