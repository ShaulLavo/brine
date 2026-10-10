package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

func (s *Store) ReadReplicaRevision(ctx context.Context, id string) (data.ReplicaRevision, error) {
	return readReplicaCursor(s.db.QueryRowContext(ctx, "SELECT canonical FROM data_replica_revisions WHERE id=?", id), 256<<10, func(record data.ReplicaRevision) bool { return record.Validate() == nil && record.ID == id })
}

// WriteReplicaRevision permits storage under a held fence, never activation.
func (s *Store) WriteReplicaRevision(ctx context.Context, previous data.RotationStage, record data.ReplicaRevision) (resultErr error) {
	if s.readOnly || record.Validate() != nil || !record.Follows(previous) {
		return ErrInvalid
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > 256<<10 {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer rollbackOnExit(tx, &resultErr)
	permit, err := readReplicaPermit(ctx, tx, record.Before.DatabaseID)
	if err != nil || permit.Replica != record.Before && permit.Replica != record.After {
		return ErrConflict
	}
	candidate := permit
	candidate.Replica = record.After
	if err = s.validateReplicaPermit(ctx, tx, candidate); err != nil {
		return err
	}
	var app string
	if err = tx.QueryRowContext(ctx, "SELECT app FROM data_incarnations WHERE id=?", permit.Database.IncarnationID).Scan(&app); err != nil || app != record.App {
		return ErrConflict
	}
	if record.Stage == data.RotationCommitted || record.Stage == data.RotationStartIssued || record.Stage == data.RotationActive {
		if permit.Replica != record.After {
			return ErrConflict
		}
	}
	if record.Stage != data.RevisionStored && permit.FenceState == "held" {
		return ErrConflict
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_credential_rotations WHERE binding_id=? AND stage NOT IN ('active','verified','superseded')", record.Before.BindingID).Scan(&pending); err != nil || pending != 0 {
		return ErrConflict
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_replica_revisions WHERE id=?", record.ID).Scan(&prior)
	if err == nil {
		if bytes.Equal(prior, raw) {
			return tx.Commit()
		}
		var old data.ReplicaRevision
		if json.Unmarshal(prior, &old) != nil || old.Validate() != nil || old.Stage != previous {
			return ErrConflict
		}
		old.Stage = record.Stage
		expected, _ := json.Marshal(old)
		if !bytes.Equal(expected, raw) {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE data_replica_revisions SET stage=?,canonical=? WHERE id=? AND stage=?", record.Stage, raw, record.ID, previous)
	} else if errors.Is(err, sql.ErrNoRows) && previous == "" && permit.Replica == record.Before {
		_, err = tx.ExecContext(ctx, "INSERT INTO data_replica_revisions(id,binding_id,stage,canonical) VALUES(?,?,?,?)", record.ID, record.Before.BindingID, record.Stage, raw)
	} else {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
