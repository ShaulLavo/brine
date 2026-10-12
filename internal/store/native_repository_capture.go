package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

type NativeCaptureRepository struct {
	state       *Store
	guard       *data.EngineLock
	stopped     *NativeStoppedPreparation
	operationID string
	binding     data.ReplicaBindingID
}

func (s *Store) NativePreparedCaptureRepository(ctx context.Context, stopped *NativeStoppedPreparation) (*NativeCaptureRepository, error) {
	if !stopped.active(s) {
		return nil, ErrConflict
	}
	capability := &NativeCaptureRepository{state: s, guard: stopped.preparation.preparation.guard, stopped: stopped, operationID: stopped.OperationID(), binding: stopped.preparation.intent.BindingID}
	if _, err := capability.Material(ctx); err != nil {
		return nil, err
	}
	return capability, nil
}
func (r *NativeCaptureRepository) Material(ctx context.Context) (NativeRepositoryMaterial, error) {
	if r == nil || r.state == nil || !r.guard.HoldsLaunch(r.state.dir, r.binding) {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	tx, err := r.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if r.stopped != nil {
		if err := checkNativeStoppedCapability(ctx, tx, r.stopped); err != nil {
			return NativeRepositoryMaterial{}, err
		}
	}
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", r.operationID))
	if err != nil || op.State != ops.Preflight || op.Kind != ops.NativeCapture && op.Kind != ops.NativePrepare {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	policy, _, err := readNativeBackupPolicy(ctx, tx, r.binding)
	reserved, reservedErr := readReservedEngine(ctx, tx, r.binding)
	if err != nil || reservedErr != nil || reserved.Archive.EpochID != policy.EpochID {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	d := reserved.Archive.Destination
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(reserved.Archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
	scope := restic.Scope{BindingID: string(r.binding), EpochID: string(policy.EpochID), RepositoryID: policy.RepositoryID, DestinationSHA256: location.SHA256(), CredentialVersion: policy.CredentialVersion, RecoveryVersion: policy.RecoveryVersion}
	material, err := readNativeRepositoryMaterial(ctx, tx, restic.TransportPoint{Scope: scope})
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	return material, tx.Commit()
}
func (r *NativeCaptureRepository) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if action != restic.ReadRepository && action != restic.WriteSnapshot || grant != "" {
		return ErrConflict
	}
	metadata, err := r.Material(ctx)
	expected := metadata.Scope
	expected.ExpiresAt = scope.ExpiresAt
	if err != nil || expected != scope || scope.ExpiresAt.After(metadata.Scope.ExpiresAt) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
