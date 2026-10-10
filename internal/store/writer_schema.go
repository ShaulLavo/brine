package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

// WriterSchema is declaration evidence from the current committed release, not
// a cached compatibility verdict. The host observes Bindings afresh with data.
// WriterCompatible; no host filesystem access occurs in the store.
type WriterSchema struct {
	ReleaseID   string
	Desired     policy.Desired
	Bindings    []data.DatabaseBinding
	Definitions []data.SchemaDefinition
	Allocations map[data.DatabaseID]data.AllocationReceipt
	Units       []target.Unit
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
	out, err := candidateWriterSchema(ctx, tx, s, id, desired)
	if err != nil {
		return WriterSchema{}, err
	}
	out.ReleaseID = releaseID
	out.Units = release.Units
	return out, tx.Commit()
}
