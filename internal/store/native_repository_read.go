package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

// NativeRepositoryRead is confined to a retained stopped, restore or archived
// source claim. It cannot create a repository or write a snapshot.
type NativeRepositoryRead struct {
	state    *Store
	stopped  *NativeStoppedPreparation
	restore  *NativeLiveRestoreExecution
	archived *NativeArchivedSource
	point    restic.TransportPoint
}

type NativeRepositoryMaterial struct {
	Scope               restic.Scope
	Location            restic.S3Location
	CredentialReference string
	CredentialRecord    CredentialRecord
	CredentialReceiptID string
	RecoveryDeliveryID  string
	RecoveryReference   string
	RecoverySHA256      string
	ArchiveCredential   *backupcredentials.ArchiveReceipt
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

func (s *Store) NativeArchivedRepository(ctx context.Context, archived *NativeArchivedSource) (*NativeRepositoryRead, error) {
	bundle, err := s.ReadNativeArchivedBundle(ctx, archived)
	if err != nil {
		return nil, err
	}
	capability := &NativeRepositoryRead{state: s, archived: archived, point: bundle.transport}
	if _, err := capability.Material(ctx); err != nil {
		return nil, err
	}
	return capability, nil
}

func (r *NativeRepositoryRead) check(ctx context.Context, tx *sql.Tx) error {
	if r.archived != nil {
		if r.archived.state != r.state || r.stopped != nil || r.restore != nil {
			return ErrConflict
		}
		pointID, err := checkNativeArchivedSourceTx(ctx, tx, r.archived)
		if err != nil {
			return err
		}
		bundle, err := readNativePreparedBundleTx(ctx, tx, pointID)
		if err != nil || bundle.transport != r.point {
			return errors.Join(ErrConflict, err)
		}
		return nil
	}
	return checkNativeStoppedCapability(ctx, tx, r.stopped)
}

func (r *NativeRepositoryRead) Material(ctx context.Context) (NativeRepositoryMaterial, error) {
	if r == nil || r.state == nil || r.archived == nil && !r.stopped.active(r.state) {
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
	if err := r.check(ctx, tx); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	material, err := r.material(ctx, tx)
	if err != nil {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	if err := r.check(ctx, tx); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	return material, tx.Commit()
}

func (r *NativeRepositoryRead) material(ctx context.Context, tx *sql.Tx) (NativeRepositoryMaterial, error) {
	if r.archived == nil {
		return readNativeRepositoryMaterial(ctx, tx, r.point)
	}
	disposition, exists, err := readArchiveDisposition(ctx, tx, r.archived.archiveID)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	if !exists || disposition.Stage != archive.Retired {
		return readNativeRepositoryMaterial(ctx, tx, r.point)
	}
	material, err := readNativeRepositoryMaterialRecords(ctx, tx, r.point)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	receipt, err := readArchiveCredentialReceipt(ctx, tx, r.archived.archiveID, r.archived.binding, data.ReplicaEpochID(r.point.Scope.EpochID))
	if err != nil || receipt.Admit(time.Now().UTC(), time.Minute) != nil || receipt.Plan.Scope.CredentialRef != material.CredentialReference {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	reserved, err := readReservedEngine(ctx, tx, r.archived.binding)
	selected := reserved.Archive
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	if selected.EpochID != data.ReplicaEpochID(r.point.Scope.EpochID) {
		_, selected, err = readNativeEpoch(ctx, tx, r.archived.binding, data.ReplicaEpochID(r.point.Scope.EpochID))
	}
	if err != nil || receipt.Plan.Scope.Destination != string(selected.Destination.Reference) {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	material.Scope.CredentialVersion = receipt.Plan.Version
	material.ArchiveCredential = &receipt
	deadline := receipt.Plan.Scope.RetainUntil
	if receipt.Plan.ExpiresAt != nil && deadline.After(*receipt.Plan.ExpiresAt) {
		deadline = *receipt.Plan.ExpiresAt
	}
	return limitNativeRepositoryMaterial(material, &deadline)
}

func readNativeRepositoryMaterial(ctx context.Context, q dataQuerier, point restic.TransportPoint) (NativeRepositoryMaterial, error) {
	material, err := readNativeRepositoryMaterialRecords(ctx, q, point)
	if err != nil {
		return NativeRepositoryMaterial{}, err
	}
	return limitNativeRepositoryMaterial(material, material.CredentialRecord.ExpiresAt)
}

func limitNativeRepositoryMaterial(material NativeRepositoryMaterial, expiry *time.Time) (NativeRepositoryMaterial, error) {
	material.Scope.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	if expiry != nil && material.Scope.ExpiresAt.After(*expiry) {
		material.Scope.ExpiresAt = *expiry
	}
	if !material.Scope.ExpiresAt.After(time.Now().UTC().Add(time.Minute)) {
		return NativeRepositoryMaterial{}, ErrConflict
	}
	return material, nil
}

func readNativeRepositoryMaterialRecords(ctx context.Context, q dataQuerier, point restic.TransportPoint) (NativeRepositoryMaterial, error) {
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
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
	policy, _, err := readNativeBackupPolicyScope(ctx, q, binding, archive.EpochID, scope.RepositoryID, 0, scope.RecoveryVersion)
	if err != nil || policy.RepositoryID != scope.RepositoryID || policy.EpochID != archive.EpochID || policy.RecoveryVersion != scope.RecoveryVersion || policy.CredentialReference != string(d.CredentialRef) || location.SHA256() != scope.DestinationSHA256 {
		return NativeRepositoryMaterial{}, errors.Join(ErrConflict, err)
	}
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE id=? AND kind='receipt'", policy.CredentialReceiptID).Scan(&raw)
	var credential CredentialRecord
	if err != nil || json.Unmarshal(raw, &credential) != nil || credential.BindingID != binding || credential.EpochID != archive.EpochID || credential.CredentialRef != policy.CredentialReference || credential.Version != policy.CredentialVersion || credential.ReceivedAt.IsZero() {
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
	if err := checkNativeBackupPolicyOperation(ctx, q, policy); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	if err := checkNativeRecoveryMaterialOperation(ctx, q, recovery); err != nil {
		return NativeRepositoryMaterial{}, err
	}
	scope.CredentialVersion = policy.CredentialVersion
	return NativeRepositoryMaterial{Scope: scope, Location: location, CredentialReference: policy.CredentialReference, CredentialReceiptID: policy.CredentialReceiptID, CredentialRecord: credential, RecoveryReference: policy.RecoveryReference, RecoveryDeliveryID: policy.RecoveryDeliveryID, RecoverySHA256: policy.RecoverySHA256}, nil
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

func readNativeMaterialOperation(ctx context.Context, q dataQuerier, id string, kind ops.Kind) (ops.Operation, json.RawMessage, error) {
	op, err := scanOperation(q.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", id))
	if err != nil || op.Kind != kind || op.State != ops.Succeeded {
		return ops.Operation{}, nil, errors.Join(ErrConflict, err)
	}
	var raw []byte
	err = q.QueryRowContext(ctx, "SELECT canonical FROM operation_outcomes WHERE operation_id=?", id).Scan(&raw)
	if err != nil {
		return ops.Operation{}, nil, errors.Join(ErrConflict, err)
	}
	outcome, err := ops.DecodeTaskOutcome(op, raw)
	if err != nil || len(outcome.Receipt) == 0 {
		return ops.Operation{}, nil, errors.Join(&IntegrityError{}, err)
	}
	return op, outcome.Receipt, nil
}

func checkNativeRecoveryMaterialOperation(ctx context.Context, q dataQuerier, record nativeRecoveryDeliveryRecord) error {
	op, raw, err := readNativeMaterialOperation(ctx, q, record.OperationID, ops.NativeRecoverySet)
	if err != nil {
		return err
	}
	receipt, err := nativecapture.DecodeRecoveryReceipt(raw)
	if err != nil || op.Requester != record.Requester || receipt.OperationID != record.OperationID || receipt.BindingID != string(record.BindingID) || receipt.EpochID != string(record.EpochID) || receipt.Reference != record.Reference || receipt.Version != record.Version || receipt.SHA256 != record.SHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func checkNativeBackupPolicyOperation(ctx context.Context, q dataQuerier, policy NativeBackupPolicy) error {
	_, raw, err := readNativeMaterialOperation(ctx, q, policy.OperationID, ops.NativeBackupActivate)
	if err != nil {
		return err
	}
	receipt, err := nativecapture.DecodeBackupPolicyReceipt(raw)
	canonical, marshalErr := json.Marshal(policy)
	if err != nil || marshalErr != nil || receipt.OperationID != policy.OperationID || receipt.BindingID != string(policy.BindingID) || receipt.EpochID != string(policy.EpochID) || receipt.PlanID != policy.RevisionPlanID || receipt.Revision != policy.Revision || receipt.PolicySHA256 != nativePointHash(canonical) || receipt.RepositoryID != policy.RepositoryID {
		return errors.Join(ErrConflict, err, marshalErr)
	}
	return nil
}
