package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/liverestore"
)

// SaveNativeLiveRestorePlan stores expectations only. Protected admission reads
// the actual point, consumer, operation, policy and current head separately.
func (s *Store) SaveNativeLiveRestorePlan(ctx context.Context, p liverestore.Plan) error {
	if s.readOnly {
		return ErrInvalid
	}
	canonical := p.Bytes()
	decoded, err := liverestore.DecodePlan(canonical)
	if err != nil || decoded.ID() != p.ID() {
		return ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = insertNativeLiveRestorePlanTx(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

func insertNativeLiveRestorePlanTx(ctx context.Context, tx *sql.Tx, p liverestore.Plan) error {
	canonical, spec := p.Bytes(), p.Spec()
	_, err := tx.ExecContext(ctx, "INSERT INTO data_native_live_restore_plans VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING",
		p.ID(), spec.Facts.Head.BindingID, spec.Safety.PointID, spec.Selected.PointID, canonical)
	if err != nil {
		return err
	}
	retained, err := readNativeLiveRestorePlanTx(ctx, tx, p.ID())
	if err != nil || !bytes.Equal(retained.Bytes(), canonical) {
		return errors.Join(&IntegrityError{}, err)
	}
	return nil
}

func readNativeLiveRestorePlanTx(ctx context.Context, q dataQuerier, id string) (liverestore.Plan, error) {
	var raw []byte
	var binding data.ReplicaBindingID
	var safety, selected string
	err := q.QueryRowContext(ctx, "SELECT binding_id,prepared_point_id,selected_point_id,canonical FROM data_native_live_restore_plans WHERE id=?", id).Scan(&binding, &safety, &selected, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return liverestore.Plan{}, ErrNotFound
	}
	if err != nil {
		return liverestore.Plan{}, err
	}
	p, err := liverestore.DecodePlan(raw)
	if err != nil || p.ID() != id {
		return liverestore.Plan{}, &IntegrityError{}
	}
	spec := p.Spec()
	if spec.Facts.Head.BindingID != binding || spec.Safety.PointID != safety || spec.Selected.PointID != selected {
		return liverestore.Plan{}, &IntegrityError{}
	}
	return p, nil
}

func (s *Store) LoadNativeLiveRestorePlan(ctx context.Context, id string) (liverestore.Plan, error) {
	return readNativeLiveRestorePlanTx(ctx, s.db, id)
}
