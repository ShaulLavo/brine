package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
)

// WriterSchema is declaration evidence from the current committed release, not
// a cached compatibility verdict. The host observes Bindings afresh with data.
// WriterCompatible; no host filesystem access occurs in the store.
type WriterSchema struct {
	ReleaseID string
	Desired   policy.Desired
	Bindings  []data.DatabaseBinding
}

// ReadWriterSchema works through OpenReadOnly and takes no host mutation lock.
// Missing active incarnation, committed release or complete database identity
// evidence refuses. The release and its declarations are read in one transaction.
func (s *Store) ReadWriterSchema(ctx context.Context, id data.AppIncarnationID) (WriterSchema, error) {
	if !data.ValidID(string(id)) {
		return WriterSchema{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WriterSchema{}, err
	}
	defer tx.Rollback()
	var app, releaseID string
	err = tx.QueryRowContext(ctx, "SELECT a.app,h.current_id FROM data_active_incarnations a JOIN release_heads h ON h.app=a.app WHERE a.incarnation_id=?", id).Scan(&app, &releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return WriterSchema{}, ErrNotFound
	}
	if err != nil {
		return WriterSchema{}, err
	}
	release, err := readRelease(ctx, tx, app, releaseID)
	if err != nil {
		return WriterSchema{}, err
	}
	_, desired, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil {
		return WriterSchema{}, err
	}
	if string(desired.Name) != app || len(desired.Databases) == 0 || desired.Runtime == nil {
		return WriterSchema{}, ErrConflict
	}
	out := WriterSchema{ReleaseID: releaseID, Desired: desired, Bindings: make([]data.DatabaseBinding, 0, len(desired.Databases))}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM data_databases WHERE incarnation_id=?", id).Scan(&count); err != nil {
		return WriterSchema{}, err
	}
	if count != len(desired.Databases) {
		return WriterSchema{}, ErrConflict
	}
	for _, declaration := range desired.Databases {
		var database data.DatabaseID
		if err = tx.QueryRowContext(ctx, "SELECT id FROM data_databases WHERE incarnation_id=? AND name=?", id, declaration.Name).Scan(&database); err != nil {
			return WriterSchema{}, ErrConflict
		}
		permit, err := readReplicaPermit(ctx, tx, database)
		if err != nil {
			return WriterSchema{}, err
		}
		if err = s.validateReplicaPermit(ctx, tx, permit); err != nil {
			return WriterSchema{}, err
		}
		binding := permit.Database
		if binding.IncarnationID != id || binding.Root != declaration.PersistentRoot || binding.MountPath != declaration.MountPath || binding.Filename != declaration.Filename || permit.Replica.Destination.Reference != declaration.BackupDestination || permit.FenceState == "held" {
			return WriterSchema{}, ErrConflict
		}
		out.Bindings = append(out.Bindings, binding)
	}
	return out, tx.Commit()
}
