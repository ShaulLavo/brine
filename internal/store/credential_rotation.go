package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func (s *Store) ReadCredentialRotation(ctx context.Context, planID string) (data.CredentialRotation, error) {
	return readReplicaCursor(s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_rotations WHERE plan_id=?", planID), 32<<10, func(record data.CredentialRotation) bool { return record.Validate() == nil && record.PlanID == planID })
}
func (s *Store) WriteCredentialRotation(ctx context.Context, previous data.RotationStage, record data.CredentialRotation) error {
	if s.readOnly || record.Validate() != nil || !record.Follows(previous) {
		return ErrInvalid
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 32<<10 {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	permit, err := readReplicaPermit(ctx, tx, record.Before.DatabaseID)
	if err != nil || permit.Replica != record.Before && permit.Replica != record.After {
		return ErrConflict
	}
	var pendingRevision int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_replica_revisions WHERE binding_id=? AND stage!='active'", record.Before.BindingID).Scan(&pendingRevision); err != nil || pendingRevision != 0 {
		return ErrConflict
	}
	var app string
	if err := tx.QueryRowContext(ctx, "SELECT app FROM data_incarnations WHERE id=?", permit.Database.IncarnationID).Scan(&app); err != nil || app != record.App {
		return ErrConflict
	}
	if record.Stage == data.RotationCommitted || record.Stage == data.RotationStartIssued || record.Stage == data.RotationActive || record.Stage == data.RotationVerified {
		if permit.Replica != record.After {
			return ErrConflict
		}
	}
	for _, fence := range permit.Fences {
		if fence.State == data.FenceHeld {
			return ErrConflict
		}
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_credential_rotations WHERE plan_id=?", record.PlanID).Scan(&prior)
	if err == nil {
		if bytes.Equal(prior, raw) {
			return tx.Commit()
		}
		var old data.CredentialRotation
		if json.Unmarshal(prior, &old) != nil || old.Stage != previous {
			return ErrConflict
		}
		old.Stage = record.Stage
		expected, _ := json.Marshal(old)
		if !bytes.Equal(expected, raw) {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE data_credential_rotations SET stage=?,canonical=? WHERE plan_id=? AND stage=?", record.Stage, raw, record.PlanID, previous)
	} else if errors.Is(err, sql.ErrNoRows) && previous == "" {
		if permit.Replica != record.Before {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO data_credential_rotations(plan_id,binding_id,stage,canonical) VALUES(?,?,?,?)", record.PlanID, record.Before.BindingID, record.Stage, raw)
	} else {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PendingCredentialRotation(ctx context.Context, binding data.ReplicaBindingID) (data.CredentialRotation, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT plan_id FROM data_credential_rotations WHERE binding_id=? AND stage NOT IN ('active','verified','superseded')", binding).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return data.CredentialRotation{}, ErrNotFound
	}
	if err != nil {
		return data.CredentialRotation{}, err
	}
	return s.ReadCredentialRotation(ctx, id)
}

// SupersedeCredentialRotation closes an expired cursor and starts its fresh
// replacement in one transaction. Failure cannot leave neither or both pending.
func (s *Store) SupersedeCredentialRotation(ctx context.Context, old, next data.CredentialRotation) error {
	if s.readOnly || old.Validate() != nil || next.Validate() != nil || !old.Expired(time.Now().UTC()) || next.Expired(time.Now().UTC()) || old.Stage == data.RotationSuperseded || next.Stage != data.RotationPrepared || next.App != old.App || next.Before.BindingID != old.Before.BindingID || next.Before.EpochID != old.Before.EpochID || next.After.CredentialVersion <= old.After.CredentialVersion || next.PlanID == old.PlanID {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var canonical []byte
	if err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_credential_rotations WHERE plan_id=?", old.PlanID).Scan(&canonical); err != nil {
		return err
	}
	expected, _ := json.Marshal(old)
	if !bytes.Equal(canonical, expected) {
		return ErrConflict
	}
	permit, err := readReplicaPermit(ctx, tx, next.Before.DatabaseID)
	if err != nil || permit.Replica != next.Before || next.Before != old.Before && next.Before != old.After {
		return ErrConflict
	}
	var pendingRevision int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_replica_revisions WHERE binding_id=? AND stage!='active'", next.Before.BindingID).Scan(&pendingRevision); err != nil || pendingRevision != 0 {
		return ErrConflict
	}
	var app string
	if err := tx.QueryRowContext(ctx, "SELECT app FROM data_incarnations WHERE id=?", permit.Database.IncarnationID).Scan(&app); err != nil || app != next.App {
		return ErrConflict
	}
	for _, fence := range permit.Fences {
		if fence.State == data.FenceHeld {
			return ErrConflict
		}
	}
	old.Stage, old.SupersededBy = data.RotationSuperseded, next.PlanID
	closed, _ := json.Marshal(old)
	fresh, _ := json.Marshal(next)
	if len(closed) > 32<<10 || len(fresh) > 32<<10 || old.Validate() != nil {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, "UPDATE data_credential_rotations SET stage=?,canonical=? WHERE plan_id=?", old.Stage, closed, old.PlanID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_credential_rotations VALUES(?,?,?,?)", next.PlanID, next.Before.BindingID, next.Stage, fresh); err != nil {
		return err
	}
	return tx.Commit()
}
