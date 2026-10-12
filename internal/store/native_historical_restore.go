package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

type NativeHistoricalRestore struct {
	state       *Store
	guard       *data.EngineLock
	operationID string
	binding     data.ReplicaBindingID
	pointID     string
}
type NativeHistoricalBundle struct {
	Point          NativeVerifiedPointView
	Source         nativecapture.Source
	Checkpoint     nativecapture.Checkpoint
	Selection      nativecapture.Selection
	Manifest       restic.Manifest
	Transport      restic.TransportPoint
	SourceEvidence nativeevidence.Evidence
	ConfirmedBound spacetimedb.ConfirmedPairsRecord
	FinalPairs     spacetimedb.RecoveredPairs
	Schema         spacetimedb.CanonicalSchema
	Material       NativeRepositoryMaterial
}

func (s *Store) ClaimNativeHistoricalRestore(ctx context.Context, operationID string) (*NativeHistoricalRestore, error) {
	input, err := s.ReadNativeTaskInput(ctx, operationID)
	if s.readOnly || err != nil || input.Kind != ops.NativeRestoreTest {
		return nil, errors.Join(ErrConflict, err)
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, input.BindingID)
	if err != nil {
		return nil, err
	}
	cap := &NativeHistoricalRestore{state: s, guard: guard, operationID: operationID, binding: input.BindingID, pointID: input.PointID}
	if _, err := cap.Bundle(ctx); err != nil {
		_ = guard.Release()
		return nil, err
	}
	return cap, nil
}
func (r *NativeHistoricalRestore) Close() error {
	if r == nil || r.guard == nil {
		return nil
	}
	return r.guard.Release()
}
func (r *NativeHistoricalRestore) Bundle(ctx context.Context) (NativeHistoricalBundle, error) {
	if r == nil || r.state == nil || !r.guard.HoldsLaunch(r.state.dir, r.binding) {
		return NativeHistoricalBundle{}, ErrConflict
	}
	tx, err := r.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeHistoricalBundle{}, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", r.operationID))
	if err != nil || op.Kind != ops.NativeRestoreTest || op.State != ops.Preflight {
		return NativeHistoricalBundle{}, errors.Join(ErrConflict, err)
	}
	input, err := readNativeTaskInput(ctx, tx, op)
	if err != nil || input.BindingID != r.binding || input.PointID != r.pointID {
		return NativeHistoricalBundle{}, ErrConflict
	}
	bundle, err := readNativeVerifiedPointBundleTx(ctx, tx, r.pointID)
	if err != nil || bundle.point.view.BindingID != r.binding || !time.Now().UTC().Before(bundle.point.view.RetainUntil) {
		return NativeHistoricalBundle{}, errors.Join(ErrConflict, err)
	}
	schema, _, err := readNativeVerifiedDependencies(ctx, tx, bundle.point, bundle.checkpoint)
	if err != nil {
		return NativeHistoricalBundle{}, err
	}
	material, err := readNativeRepositoryMaterial(ctx, tx, bundle.transport)
	if err != nil {
		return NativeHistoricalBundle{}, err
	}
	point := bundle.point.view
	point.PointSHA256 = bundle.point.hash
	result := NativeHistoricalBundle{Point: point, Source: bundle.source, Checkpoint: bundle.checkpoint, Selection: bundle.selection, Manifest: bundle.manifest, Transport: bundle.transport, SourceEvidence: bundle.sourceEvidence, ConfirmedBound: bundle.confirmedBound, FinalPairs: bundle.finalPairs, Schema: schema, Material: material}
	return result, tx.Commit()
}
func (r *NativeHistoricalRestore) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if action != restic.ReadRepository || grant != "" {
		return ErrConflict
	}
	bundle, err := r.Bundle(ctx)
	expected := bundle.Material.Scope
	expected.ExpiresAt = scope.ExpiresAt
	if err != nil || expected != scope || scope.ExpiresAt.After(bundle.Material.Scope.ExpiresAt) || !scope.ExpiresAt.After(time.Now().UTC()) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
