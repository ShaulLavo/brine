package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/ops"
)

const cdnPlanLimit = 1 << 20

func (s *Store) SaveCDNPlan(ctx context.Context, p cdn.Plan) (cdn.PlanID, error) {
	raw, err := p.Encoded()
	if err != nil || len(raw) > cdnPlanLimit || validateCDNInitial(p) != nil {
		return "", ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM cdn_plans WHERE id=?", p.ID).Scan(&old)
	if err == nil {
		if !bytes.Equal(old, raw) {
			return "", &IntegrityError{}
		}
		return p.ID, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	a := p.Context.Attachment
	_, err = tx.ExecContext(ctx, "INSERT INTO cdn_plans VALUES(?,?,?,?,?)", p.ID, a.Owner.App, a.Binding.Hostname, a.Owner.Attachment, raw)
	if err != nil {
		return "", err
	}
	return p.ID, tx.Commit()
}

func validateCDNInitial(p cdn.Plan) error {
	a := p.Context.Attachment
	seen := map[cdn.ProfileScope]bool{}
	for _, observed := range p.Context.Initial {
		if !observed.Profile.Valid() || observed.ObservedAt.IsZero() || !digestPattern.MatchString(string(observed.Fingerprint)) || seen[observed.Profile] {
			return ErrInvalid
		}
		seen[observed.Profile] = true
	}
	if !seen[a.Profile] || !seen[a.Binding.Traffic] {
		return ErrInvalid
	}
	return nil
}

func loadCDNPlan(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id cdn.PlanID) (cdn.Plan, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT canonical FROM cdn_plans WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cdn.Plan{}, ErrNotFound
	}
	if err != nil {
		return cdn.Plan{}, err
	}
	var p cdn.Plan
	if len(raw) > cdnPlanLimit || json.Unmarshal(raw, &p) != nil || p.ID != id {
		return p, &IntegrityError{}
	}
	canonical, err := p.Encoded()
	if err != nil || !bytes.Equal(raw, canonical) || validateCDNInitial(p) != nil {
		return p, &IntegrityError{}
	}
	return p, nil
}

func (s *Store) LoadCDNPlan(ctx context.Context, id cdn.PlanID) (cdn.Plan, error) {
	return loadCDNPlan(ctx, s.db, id)
}

func loadCDNStatus(ctx context.Context, tx *sql.Tx, operation string) (cdn.Status, ops.Operation, error) {
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", operation))
	if err != nil {
		return cdn.Status{}, op, err
	}
	if op.Kind != ops.CDN {
		return cdn.Status{}, op, ErrInvalid
	}
	p, err := loadCDNPlan(ctx, tx, cdn.PlanID(op.SecretRef))
	if err != nil {
		return cdn.Status{}, op, err
	}
	status := cdn.Status{Plan: p, Operation: cdn.OperationID(operation), State: string(op.State), Effects: []cdn.Effect{}}
	var proof, readback []byte
	if err = tx.QueryRowContext(ctx, "SELECT proof,readback FROM cdn_sessions WHERE operation_id=? AND plan_id=?", operation, p.ID).Scan(&proof, &readback); err != nil {
		return status, op, err
	}
	if len(proof) != 0 && json.Unmarshal(proof, &status.Proof) != nil || len(readback) != 0 && json.Unmarshal(readback, &status.Readback) != nil {
		return status, op, &IntegrityError{}
	}
	rows, err := tx.QueryContext(ctx, "SELECT step_index,permit,record FROM cdn_effects WHERE operation_id=? ORDER BY step_index", operation)
	if err != nil {
		return status, op, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var index int
		var permitRaw, record []byte
		var effect cdn.Effect
		if rows.Scan(&index, &permitRaw, &record) != nil || index != len(status.Effects) || json.Unmarshal(record, &effect) != nil || index >= len(p.Steps) {
			return status, op, &IntegrityError{}
		}
		// #nosec G115 -- The index is nonnegative and below the validated MaxSteps bound.
		permit, e := p.NewPermit(cdn.OperationID(operation), uint32(index), effect.Permit.Action)
		permit.Claim = effect.Permit.Claim
		encoded, _ := json.Marshal(effect.Permit)
		if e != nil || !reflect.DeepEqual(permit, effect.Permit) || !bytes.Equal(encoded, permitRaw) || permit.Claim == "" {
			return status, op, &IntegrityError{}
		}
		status.Effects = append(status.Effects, effect)
	}
	return status, op, rows.Err()
}

func (s *Store) CDNStatus(ctx context.Context, operation string) (cdn.Status, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return cdn.Status{}, err
	}
	defer func() { _ = tx.Rollback() }()
	status, _, err := loadCDNStatus(ctx, tx, operation)
	return status, err
}

func (s *Store) CDNHead(ctx context.Context, app string) (cdn.Revision, error) {
	return readCDNRevision(ctx, s.db, app, "")
}

func (s *Store) CDNRevision(ctx context.Context, app string, id cdn.RevisionID) (cdn.Revision, error) {
	if id == "" {
		return cdn.Revision{}, ErrInvalid
	}
	return readCDNRevision(ctx, s.db, app, id)
}

func readCDNRevision(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, app string, id cdn.RevisionID) (cdn.Revision, error) {
	query := "SELECT canonical FROM cdn_revisions WHERE app=? AND id=?"
	if id == "" {
		query = "SELECT r.canonical FROM cdn_heads h JOIN cdn_revisions r ON r.id=h.revision_id WHERE h.app=?"
	}
	args := []any{app}
	if id != "" {
		args = append(args, id)
	}
	var raw []byte
	err := q.QueryRowContext(ctx, query, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cdn.Revision{}, ErrNotFound
	}
	if err != nil {
		return cdn.Revision{}, err
	}
	var revision cdn.Revision
	if json.Unmarshal(raw, &revision) != nil || revision.Attachment.Validate() != nil || revision.Attachment.Owner.App != app || id != "" && revision.Attachment.Revision != id {
		return revision, &IntegrityError{}
	}
	return revision, nil
}

func freshCDNObservation(observed cdn.Observation, since time.Time) bool {
	return !observed.ObservedAt.Before(since) && !observed.ObservedAt.After(time.Now().Add(time.Minute))
}
