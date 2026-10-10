package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"strings"
)

func (s *Store) SaveRestorePoint(ctx context.Context, p data.RestorePoint) error {
	if s.readOnly || p.Validate() != nil {
		return ErrInvalid
	}
	binding, epoch := p.BindingID, p.EpochID
	if !data.ValidID(string(binding)) || !data.ValidID(string(epoch)) {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 16<<10 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var database data.DatabaseID
	if err := tx.QueryRowContext(ctx, "SELECT database_id FROM data_replica_bindings WHERE id=?", binding).Scan(&database); err != nil {
		return err
	}
	permit, err := readReplicaPermit(ctx, tx, database)
	if err != nil || !permit.Replica.Committed || permit.Replica.EpochID != epoch || p.Schema.DatabaseID != permit.Database.DatabaseID {
		return ErrConflict
	}
	if source := p.Snapshot; source != nil && source.ObjectKey != strings.TrimSuffix(permit.Replica.RemotePrefix, "/")+"/restore-points/"+p.ID+"/snapshot.sqlite" {
		return ErrInvalid
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=?", p.ID).Scan(&old)
	if err == nil {
		if !bytes.Equal(old, raw) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_restore_points(id,binding_id,epoch_id,canonical) VALUES(?,?,?,?)", p.ID, binding, epoch, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ReadRestorePoint works through OpenReadOnly and refuses another binding/epoch.
func (s *Store) ReadRestorePoint(ctx context.Context, id string, binding data.ReplicaBindingID, epoch data.ReplicaEpochID) (data.RestorePoint, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_restore_points WHERE id=? AND binding_id=? AND epoch_id=?", id, binding, epoch).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return data.RestorePoint{}, ErrNotFound
	}
	if err != nil {
		return data.RestorePoint{}, err
	}
	var point data.RestorePoint
	if len(raw) > 16<<10 || json.Unmarshal(raw, &point) != nil || point.Validate() != nil || point.ID != id {
		return data.RestorePoint{}, &IntegrityError{}
	}
	b, e := point.BindingID, point.EpochID
	canonical, _ := json.Marshal(point)
	if b != binding || e != epoch || !bytes.Equal(canonical, raw) {
		return data.RestorePoint{}, &IntegrityError{}
	}
	return point, nil
}
