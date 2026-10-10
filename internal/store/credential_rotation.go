package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

func (s *Store) ReadCredentialRotation(ctx context.Context, planID string) (data.CredentialRotation, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_rotations WHERE plan_id=?", planID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return data.CredentialRotation{}, ErrNotFound
	}
	if err != nil {
		return data.CredentialRotation{}, err
	}
	var record data.CredentialRotation
	if len(raw) > 32<<10 || json.Unmarshal(raw, &record) != nil || record.Validate() != nil || record.PlanID != planID {
		return data.CredentialRotation{}, &IntegrityError{}
	}
	canonical, _ := json.Marshal(record)
	if !bytes.Equal(raw, canonical) {
		return data.CredentialRotation{}, &IntegrityError{}
	}
	return record, nil
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
