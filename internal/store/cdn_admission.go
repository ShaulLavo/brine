package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (s *Store) AdmitCDN(ctx context.Context, id cdn.PlanID, requester, key string) (ops.Operation, bool, error) {
	if len(requester) == 0 || len(requester) > 256 || len(key) == 0 || len(key) > 256 {
		return ops.Operation{}, false, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return ops.Operation{}, false, err
	}
	defer tx.Rollback()
	p, err := loadCDNPlan(ctx, tx, id)
	if err != nil {
		return ops.Operation{}, false, err
	}
	old, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE requester=? AND idempotency_key=?", requester, key))
	if err == nil {
		if old.Kind != ops.CDN || old.SecretRef != string(id) || old.App != p.Context.Attachment.Owner.App {
			return old, false, ErrConflict
		}
		return old, true, tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return old, false, err
	}
	if err = admitCDNContext(ctx, tx, p); err != nil {
		return old, false, err
	}
	if err = releaseCDNRecovery(ctx, tx, p); err != nil {
		return old, false, err
	}
	var active int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM operations WHERE app=? AND state NOT IN ('succeeded','failed','rolled_back','recovery_required')", p.Context.Attachment.Owner.App).Scan(&active)
	if err != nil {
		return old, false, err
	}
	if active != 0 {
		return old, false, ErrConflict
	}
	opID, err := newID()
	if err != nil {
		return old, false, err
	}
	now := timestamp()
	app := p.Context.Attachment.Owner.App
	_, err = tx.ExecContext(ctx, "INSERT INTO operations VALUES(?,NULL,?,?,?,?,?,?,?,?,?)", opID, requester, key, ops.Queued, now, now, ops.CDN, app, string(id), "")
	if err != nil {
		return old, false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO cdn_sessions(operation_id,plan_id) VALUES(?,?)", opID, id)
	if err != nil {
		return old, false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO cdn_reservations VALUES(?,?,?,?)", app, p.Context.Attachment.Binding.Hostname, p.Context.Attachment.Owner.Attachment, opID)
	if err != nil {
		return old, false, ErrConflict
	}
	if err = cdnState(ctx, tx, opID, ops.Preparing); err != nil {
		return old, false, err
	}
	if err = cdnEvent(ctx, tx, opID, p.ID, "", "admitted", ""); err != nil {
		return old, false, err
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", opID))
	if err != nil {
		return old, false, err
	}
	return op, false, tx.Commit()
}

func admitCDNContext(ctx context.Context, tx *sql.Tx, p cdn.Plan) error {
	a := p.Context.Attachment
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT current_id FROM release_heads WHERE app=?", a.Owner.App).Scan(&current); err != nil {
		return ErrConflict
	}
	if current != a.SourceRelease {
		return ErrConflict
	}
	release, err := readRelease(ctx, tx, a.Owner.App, current)
	if err != nil {
		return err
	}
	_, desired, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil {
		return err
	}
	contains := false
	for _, domain := range desired.Domains {
		if string(domain) == a.Binding.Hostname {
			contains = true
		}
	}
	if !contains {
		return ErrConflict
	}
	head, err := readCDNRevision(ctx, tx, a.Owner.App, "")
	if errors.Is(err, ErrNotFound) {
		if p.Context.Previous != "" || len(p.Context.Retained) != 0 || a.Owner != cdn.AttachmentFor(a.Binding.Target, a.Owner.App, a.SourceRelease) {
			return ErrConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	if p.Context.Previous != head.Attachment.Revision || a.Owner != head.Attachment.Owner || a.Binding.Hostname != head.Attachment.Binding.Hostname || !reflect.DeepEqual(p.Context.Retained, head.Receipts) {
		return ErrConflict
	}
	return nil
}

func releaseCDNRecovery(ctx context.Context, tx *sql.Tx, p cdn.Plan) error {
	if p.Context.RecoveryOf == "" {
		return nil
	}
	status, source, err := loadCDNStatus(ctx, tx, string(p.Context.RecoveryOf))
	if err != nil {
		return err
	}
	if source.State != ops.Preparing || status.Plan.Context.Attachment.Owner != p.Context.Attachment.Owner {
		return ErrConflict
	}
	for _, effect := range status.Effects {
		if effect.Stage != cdn.Confirmed && effect.Stage != cdn.Rejected {
			return ErrConflict
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM cdn_reservations WHERE operation_id=?", source.ID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if err = cdnState(ctx, tx, source.ID, ops.Failed); err != nil {
		return err
	}
	return cdnEvent(ctx, tx, source.ID, status.Plan.ID, "", "superseded", "")
}

func cdnState(ctx context.Context, tx *sql.Tx, operation string, state ops.State) error {
	if _, err := tx.ExecContext(ctx, "UPDATE operations SET state=?,updated_at=? WHERE id=?", state, timestamp(), operation); err != nil {
		return err
	}
	_, err := appendEvent(ctx, tx, operation, ops.Event{Kind: "state", State: state})
	return err
}

func cdnEvent(ctx context.Context, tx *sql.Tx, operation string, plan cdn.PlanID, effect cdn.EffectID, stage string, evidence cdn.Fingerprint) error {
	raw, _ := json.Marshal(ops.CDNPayload{PlanID: string(plan), Effect: string(effect), Stage: stage, Evidence: string(evidence)})
	_, err := appendEvent(ctx, tx, operation, ops.Event{Kind: "cdn", Payload: raw})
	return err
}

func cdnEvidence(ctx context.Context, tx *sql.Tx, operation, kind string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > cdnPlanLimit {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO cdn_evidence SELECT ?,COALESCE(MAX(seq),0)+1,?,? FROM cdn_evidence WHERE operation_id=?", operation, kind, raw, operation)
	return err
}
