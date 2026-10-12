package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

// NativeRepositoryRead is confined to the actual retained stopped or restore
// claim. It cannot create a repository or write a snapshot.
type NativeRepositoryRead struct {
	state   *Store
	stopped *NativeStoppedPreparation
	restore *NativeLiveRestoreExecution
	point   restic.TransportPoint
}

type NativeRepositoryMaterial struct {
	Scope               restic.Scope
	Location            restic.S3Location
	CredentialReference string
	CredentialReceiptID string
	RecoveryReference   string
	RecoverySHA256      string
}

func (s *Store) NativePreparedRepository(ctx context.Context, stopped *NativeStoppedPreparation) (*NativeRepositoryRead, error) {
	bundle, err := s.ReadNativePreparedBundle(ctx, stopped)
	if err != nil {
		return nil, err
	}
	capability := &NativeRepositoryRead{state: s, stopped: stopped, point: bundle.transport}
	if _, err := capability.Material(ctx); err != nil {
		return nil, err
	}
	return capability, nil
}

func (s *Store) NativeLiveRestoreRepository(ctx context.Context, restore *NativeLiveRestoreExecution) (*NativeRepositoryRead, error) {
	bundle, err := s.ReadNativeLiveRestoreBundle(ctx, restore)
	if err != nil {
		return nil, err
	}
	capability := &NativeRepositoryRead{state: s, stopped: restore.stopped, restore: restore, point: bundle.Transport}
	if _, err := capability.Material(ctx); err != nil {
		return nil, err
	}
	return capability, nil
}

func (r *NativeRepositoryRead) Material(ctx context.Context) (NativeRepositoryMaterial, error) {
	if r == nil || r.state == nil || !r.stopped.active(r.state) {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	if r.restore != nil && r.state.CheckNativeLiveRestoreExecution(ctx, r.restore) != nil {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	tx, err := r.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkNativeStoppedCapability(ctx, tx, r.stopped); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	material, err := readNativeRepositoryMaterial(ctx, tx, r.point)
	if err != nil || !r.stopped.active(r.state) {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	return material, tx.Commit()
}

func readNativeRepositoryMaterial(ctx context.Context, q dataQuerier, point restic.TransportPoint) (NativeRepositoryMaterial, error) {
	scope := point.Scope
	binding := data.ReplicaBindingID(scope.BindingID)
	reserved, err := readReservedEngine(ctx, q, binding)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	archive := reserved.Archive
	if archive.EpochID != data.ReplicaEpochID(scope.EpochID) {
		_, archive, err = readNativeEpoch(ctx, q, binding, data.ReplicaEpochID(scope.EpochID))
		if err != nil {
			return NativeRepositoryMaterial{}, err
		}
	}
	d := archive.Destination
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: archive.RemotePrefix, Region: d.Region, PathStyle: d.PathStyle}
	policy, _, err := readNativeBackupPolicyScope(ctx, q, binding, archive.EpochID, scope.RepositoryID, scope.CredentialVersion, scope.RecoveryVersion)
	if err != nil || policy.RepositoryID != scope.RepositoryID || policy.EpochID != archive.EpochID || policy.CredentialVersion != scope.CredentialVersion || policy.RecoveryVersion != scope.RecoveryVersion || policy.CredentialReference != string(d.CredentialRef) || location.SHA256() != scope.DestinationSHA256 {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE id=? AND kind='receipt'", policy.CredentialReceiptID).Scan(&raw)
	var credential CredentialRecord
	if err != nil || json.Unmarshal(raw, &credential) != nil || credential.BindingID != binding || credential.EpochID != archive.EpochID || credential.CredentialRef != policy.CredentialReference || credential.Version != scope.CredentialVersion || credential.ReceivedAt.IsZero() {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	credentialCanonical, marshalErr := json.Marshal(credential)
	if marshalErr != nil || !bytes.Equal(raw, credentialCanonical) || credential.ID != policy.CredentialReceiptID || credential.Kind != "receipt" {
		return NativeRepositoryMaterial{}, &IntegrityError{}
	}
	var recoveryRaw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_recovery_deliveries WHERE id=?", policy.RecoveryDeliveryID).Scan(&recoveryRaw)
	var recovery nativeRecoveryDeliveryRecord
	if err != nil || json.Unmarshal(recoveryRaw, &recovery) != nil || recovery.BindingID != binding || recovery.EpochID != archive.EpochID || recovery.Reference != policy.RecoveryReference || recovery.Version != scope.RecoveryVersion || recovery.SHA256 != policy.RecoverySHA256 {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	recoveryCanonical, marshalErr := json.Marshal(recovery)
	if marshalErr != nil || !bytes.Equal(recoveryRaw, recoveryCanonical) || recovery.Format != "brine-native-recovery-delivery-v1" || recovery.ID != policy.RecoveryDeliveryID || recovery.ReceivedAt.IsZero() || recovery.ReceivedAt.Location() != time.UTC {
		return NativeRepositoryMaterial{}, &IntegrityError{}
	}
	if err := checkNativeMaterialOperation(ctx, q, policy.OperationID, ops.NativeBackupActivate); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	if err := checkNativeMaterialOperation(ctx, q, recovery.OperationID, ops.NativeRecoverySet); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	scope.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	if credential.ExpiresAt != nil && scope.ExpiresAt.After(*credential.ExpiresAt) {
		scope.ExpiresAt = *credential.ExpiresAt
	}
	if !scope.ExpiresAt.After(time.Now().UTC().Add(time.Minute)) {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	return NativeRepositoryMaterial{Scope: scope, Location: location, CredentialReference: policy.CredentialReference, CredentialReceiptID: policy.CredentialReceiptID, RecoveryReference: policy.RecoveryReference, RecoverySHA256: policy.RecoverySHA256}, nil
}

func (r *NativeRepositoryRead) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if action != restic.ReadRepository || grant != "" {
		return ErrConflict
	}
	material, err := r.Material(ctx)
	expected := material.Scope
	expected.ExpiresAt = scope.ExpiresAt
	if err != nil || expected != scope || !scope.ExpiresAt.After(time.Now().UTC()) || scope.ExpiresAt.After(material.Scope.ExpiresAt) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func checkNativeMaterialOperation(ctx context.Context, q dataQuerier, id string, kind ops.Kind) error {
	var actual ops.Kind
	var state ops.State
	err := q.QueryRowContext(ctx, "SELECT kind,state FROM operations WHERE id=?", id).Scan(&actual, &state)
	if err != nil || actual != kind || state != ops.Succeeded {
		return errors.Join(ErrConflict, err)
	}
	return nil
}
