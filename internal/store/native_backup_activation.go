package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

type NativeBackupActivation struct {
	state       *Store
	guard       *data.EngineLock
	operationID string
	plan        NativeBackupRevisionPlan
	claim       []byte
}

func (a *NativeBackupActivation) active() bool {
	return a != nil && a.state != nil && a.guard.HoldsLaunch(a.state.dir, a.plan.BindingID)
}
func (a *NativeBackupActivation) Close() error {
	if a == nil || a.guard == nil {
		return nil
	}
	return a.guard.Release()
}

type NativeBackupActivationMetadata struct {
	Plan                NativeBackupRevisionPlan
	Material            NativeRepositoryMaterial
	Previous            NativeBackupPolicy
	CreationGrantSHA256 string
}

type nativeBackupClaim struct {
	OperationID string    `json:"operation_id"`
	PlanID      string    `json:"plan_id"`
	PlanSHA256  string    `json:"plan_sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Store) ClaimNativeBackupActivation(ctx context.Context, operationID string) (*NativeBackupActivation, error) {
	op, err := s.GetOperation(ctx, operationID)
	if s.readOnly || err != nil || op.Kind != ops.NativeBackupActivate || op.State != ops.Preflight {
		return nil, errors.Join(ErrConflict, err)
	}
	plan, err := s.ReadNativeBackupRevisionPlan(ctx, op.SecretRef)
	if err != nil || plan.Requester != op.Requester || plan.App != op.App {
		return nil, errors.Join(ErrConflict, err)
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, plan.BindingID)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = guard.Release()
		}
	}()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := readNativeBackupRevisionPlan(ctx, tx, plan.ID)
	if err != nil {
		return nil, err
	}
	planRaw, _ := json.Marshal(current)
	claim := nativeBackupClaim{OperationID: operationID, PlanID: plan.ID, PlanSHA256: nativePointHash(planRaw), CreatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(claim)
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_revision_claims WHERE plan_id=?", plan.ID).Scan(&existing)
	if err == nil {
		return nil, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_revision_claims VALUES(?,?,?)", plan.ID, operationID, raw)
	if err != nil {
		return nil, err
	}
	capability := &NativeBackupActivation{state: s, guard: guard, operationID: operationID, plan: current, claim: raw}
	if _, err := nativeBackupActivationMetadataTx(ctx, tx, capability); err != nil {
		return nil, err
	}
	if err := appendNativeBackupEvent(ctx, tx, capability, "prepared", raw); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	complete = true
	return capability, nil
}

func nativeBackupActivationMetadataTx(ctx context.Context, tx *sql.Tx, a *NativeBackupActivation) (NativeBackupActivationMetadata, error) {
	if !a.active() {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	plan, err := readNativeBackupRevisionPlan(ctx, tx, a.plan.ID)
	if err != nil {
		return NativeBackupActivationMetadata{}, err
	}
	var claim []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_revision_claims WHERE plan_id=? AND operation_id=?", plan.ID, a.operationID).Scan(&claim)
	op, opErr := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", a.operationID))
	if err != nil || !bytes.Equal(claim, a.claim) || opErr != nil || op.Kind != ops.NativeBackupActivate || op.State != ops.Preflight || op.Requester != plan.Requester || op.SecretRef != plan.ID || op.App != plan.App {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	reserved, err := readReservedEngine(ctx, tx, plan.BindingID)
	if err != nil || reserved.Archive.EpochID != plan.EpochID {
		return NativeBackupActivationMetadata{}, errors.Join(ErrConflict, err)
	}
	var credentialRaw, recoveryRaw []byte
	d := reserved.Archive.Destination
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE binding_id=? AND epoch_id=? AND kind='receipt' AND json_extract(canonical,'$.credential_ref')=? ORDER BY json_extract(canonical,'$.version') DESC LIMIT 1", plan.BindingID, plan.EpochID, d.CredentialRef).Scan(&credentialRaw)
	var credential CredentialRecord
	if err != nil || json.Unmarshal(credentialRaw, &credential) != nil {
		return NativeBackupActivationMetadata{}, errors.Join(ErrConflict, err)
	}
	canonical, _ := json.Marshal(credential)
	if !bytes.Equal(canonical, credentialRaw) || credential.BindingID != plan.BindingID || credential.EpochID != plan.EpochID || credential.CredentialRef != d.CredentialRef || credential.Version == 0 || credential.Kind != "receipt" || credential.ReceivedAt.IsZero() {
		return NativeBackupActivationMetadata{}, &IntegrityError{}
	}
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_recovery_deliveries WHERE binding_id=? AND epoch_id=? AND reference=? ORDER BY version DESC LIMIT 1", plan.BindingID, plan.EpochID, plan.RecoveryReference).Scan(&recoveryRaw)
	var recovery nativeRecoveryDeliveryRecord
	if err != nil || json.Unmarshal(recoveryRaw, &recovery) != nil || recovery.BindingID != plan.BindingID || recovery.EpochID != plan.EpochID || recovery.Reference != plan.RecoveryReference || checkNativeMaterialOperation(ctx, tx, recovery.OperationID, ops.NativeRecoverySet) != nil {
		return NativeBackupActivationMetadata{}, errors.Join(ErrConflict, err)
	}
	canonical, _ = json.Marshal(recovery)
	if !bytes.Equal(canonical, recoveryRaw) || recovery.Version == 0 || !data.ValidCatalogHash(recovery.SHA256) || recovery.ReceivedAt.IsZero() {
		return NativeBackupActivationMetadata{}, &IntegrityError{}
	}
	previous, _, err := readNativeBackupPolicy(ctx, tx, plan.BindingID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return NativeBackupActivationMetadata{}, err
	}
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(reserved.Archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
	scope := restic.Scope{BindingID: string(plan.BindingID), EpochID: string(plan.EpochID), RepositoryID: previous.RepositoryID, DestinationSHA256: location.SHA256(), CredentialVersion: credential.Version, RecoveryVersion: recovery.Version, ExpiresAt: plan.ExpiresAt}
	if credential.ExpiresAt != nil && scope.ExpiresAt.After(*credential.ExpiresAt) {
		scope.ExpiresAt = *credential.ExpiresAt
	}
	if !scope.ExpiresAt.After(time.Now().UTC().Add(time.Minute)) {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	if previous.Revision != 0 && (previous.RecoveryVersion != recovery.Version || previous.RecoveryReference != recovery.Reference) {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	return NativeBackupActivationMetadata{Plan: plan, Previous: previous, CreationGrantSHA256: nativePointHash(a.claim), Material: NativeRepositoryMaterial{Scope: scope, Location: location, CredentialReference: credential.CredentialRef, CredentialRecord: credential, CredentialReceiptID: credential.ID, RecoveryReference: recovery.Reference, RecoveryDeliveryID: recovery.ID, RecoverySHA256: recovery.SHA256}}, nil
}

func (a *NativeBackupActivation) Metadata(ctx context.Context) (NativeBackupActivationMetadata, error) {
	if !a.active() {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	tx, err := a.state.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return NativeBackupActivationMetadata{}, err
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err := nativeBackupActivationMetadataTx(ctx, tx, a)
	if err != nil {
		return NativeBackupActivationMetadata{}, err
	}
	return metadata, tx.Commit()
}

type nativeBackupInitEvent struct {
	OperationID  string       `json:"operation_id"`
	Stage        string       `json:"stage"`
	Scope        restic.Scope `json:"scope"`
	GrantSHA256  string       `json:"grant_sha256"`
	RepositoryID string       `json:"repository_id"`
	ConfigSHA256 string       `json:"config_sha256"`
	VerifiedAt   time.Time    `json:"verified_at"`
}

func (a *NativeBackupActivation) BeginInitialization(ctx context.Context) error {
	metadata, err := a.Metadata(ctx)
	if err != nil || metadata.Previous.Revision != 0 {
		return errors.Join(ErrConflict, err)
	}
	tx, cancel, err := a.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err = nativeBackupActivationMetadataTx(ctx, tx, a)
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM data_native_backup_initialization_events WHERE operation_id=?", a.operationID).Scan(&count); err != nil || count != 0 {
		return ErrConflict
	}
	event := nativeBackupInitEvent{OperationID: a.operationID, Stage: "issued", Scope: metadata.Material.Scope, GrantSHA256: metadata.CreationGrantSHA256}
	raw, _ := json.Marshal(event)
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_backup_initialization_events VALUES(?,1,'issued',?)", a.operationID, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *NativeBackupActivation) AdmitRepository(ctx context.Context, scope restic.Scope, action restic.RepositoryAction, grant string) error {
	if !a.active() || action != restic.ReadRepository && action != restic.CreateRepository {
		return ErrConflict
	}
	tx, cancel, err := a.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err := nativeBackupActivationMetadataTx(ctx, tx, a)
	if err != nil || scope != metadata.Material.Scope {
		return errors.Join(ErrConflict, err)
	}
	if metadata.Previous.Revision != 0 {
		if action != restic.ReadRepository || grant != "" {
			return ErrConflict
		}
		return tx.Commit()
	}
	var sequence uint64
	var stage string
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT sequence,stage,canonical FROM data_native_backup_initialization_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", a.operationID).Scan(&sequence, &stage, &raw)
	var event nativeBackupInitEvent
	if err != nil || json.Unmarshal(raw, &event) != nil || event.OperationID != a.operationID || event.Scope != scope || event.GrantSHA256 != grant || grant != metadata.CreationGrantSHA256 || event.Stage != stage || stage == "initialized" {
		return errors.Join(ErrConflict, err)
	}
	if action == restic.CreateRepository {
		if sequence != 1 || stage != "issued" {
			return ErrConflict
		}
		event.Stage = "unknown"
		raw, _ = json.Marshal(event)
		if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_backup_initialization_events VALUES(?,2,'unknown',?)", a.operationID, raw); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *NativeBackupActivation) RecordInitialized(ctx context.Context, proof restic.InitializedRepository) error {
	if !a.active() {
		return ErrConflict
	}
	tx, cancel, err := a.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err := nativeBackupActivationMetadataTx(ctx, tx, a)
	if err != nil || metadata.Previous.Revision != 0 || !proof.AdmitsCreation(metadata.Material.Scope, metadata.CreationGrantSHA256) {
		return errors.Join(ErrConflict, err)
	}
	var stage string
	if err := tx.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_initialization_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", a.operationID).Scan(&stage); err != nil || stage != "unknown" {
		return ErrConflict
	}
	event := nativeBackupInitEvent{OperationID: a.operationID, Stage: "initialized", Scope: metadata.Material.Scope, GrantSHA256: metadata.CreationGrantSHA256, RepositoryID: proof.Scope().RepositoryID, ConfigSHA256: proof.ConfigurationSHA256(), VerifiedAt: proof.VerifiedAt()}
	raw, _ := json.Marshal(event)
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_backup_initialization_events VALUES(?,3,'initialized',?)", a.operationID, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func appendNativeBackupEvent(ctx context.Context, tx *sql.Tx, a *NativeBackupActivation, stage string, proof []byte) error {
	var sequence uint64
	var previous []byte
	err := tx.QueryRowContext(ctx, "SELECT sequence,canonical FROM data_native_backup_revision_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", a.operationID).Scan(&sequence, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if sequence == 0 {
		previous = a.claim
	}
	raw, _ := json.Marshal(struct {
		OperationID string          `json:"operation_id"`
		Stage       string          `json:"stage"`
		Proof       json.RawMessage `json:"proof"`
		At          time.Time       `json:"at"`
	}{a.operationID, stage, proof, time.Now().UTC()})
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_revision_events VALUES(?,?,?,?,?)", a.operationID, sequence+1, stage, nativePointHash(previous), raw)
	return err
}

func (a *NativeBackupActivation) BeginUnits(ctx context.Context) error {
	if !a.active() {
		return ErrConflict
	}
	tx, cancel, err := a.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := nativeBackupActivationMetadataTx(ctx, tx, a); err != nil {
		return err
	}
	var stage string
	if err := tx.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_revision_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", a.operationID).Scan(&stage); err != nil || stage != "prepared" {
		return ErrConflict
	}
	if err := appendNativeBackupEvent(ctx, tx, a, "units_issued", []byte(`{"effect":"fixed_native_backup_units"}`)); err != nil {
		return err
	}
	return tx.Commit()
}

type NativeBackupActivationObservation struct {
	RepositoryID       string
	UnitSHA256         string
	TimerSHA256        string
	RecoveryDeliveryID string
}

func (NativeBackupActivationObservation) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

func (a *NativeBackupActivation) Commit(ctx context.Context, observed NativeBackupActivationObservation) (nativecapture.BackupPolicyReceipt, error) {
	if !a.active() {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	tx, cancel, err := a.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativecapture.BackupPolicyReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	metadata, err := nativeBackupActivationMetadataTx(ctx, tx, a)
	if err != nil || !data.ValidCatalogHash(observed.RepositoryID) || !data.ValidCatalogHash(observed.UnitSHA256) || !data.ValidCatalogHash(observed.TimerSHA256) {
		return nativecapture.BackupPolicyReceipt{}, errors.Join(ErrConflict, err)
	}
	if metadata.Previous.Revision == 0 {
		var raw []byte
		var initialized nativeBackupInitEvent
		err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_initialization_events WHERE operation_id=? AND stage='initialized'", a.operationID).Scan(&raw)
		if err != nil || json.Unmarshal(raw, &initialized) != nil || initialized.RepositoryID != observed.RepositoryID || initialized.Scope != metadata.Material.Scope || !data.ValidCatalogHash(initialized.ConfigSHA256) {
			return nativecapture.BackupPolicyReceipt{}, ErrConflict
		}
	} else if metadata.Previous.RepositoryID != observed.RepositoryID {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	var deliveryID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM data_native_recovery_deliveries WHERE binding_id=? AND epoch_id=? AND reference=? AND version=?", metadata.Plan.BindingID, metadata.Plan.EpochID, metadata.Material.RecoveryReference, metadata.Material.Scope.RecoveryVersion).Scan(&deliveryID)
	if err != nil || deliveryID != observed.RecoveryDeliveryID {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	p, m := metadata.Plan, metadata.Material
	policy := NativeBackupPolicy{Format: "brine-native-backup-policy-v1", BindingID: p.BindingID, EpochID: p.EpochID, Revision: p.PreviousRevision + 1, OperationID: a.operationID, RevisionPlanID: p.ID, DeploymentPlanID: p.DeploymentPlanID, DesiredSHA256: p.DesiredSHA256, PolicySHA256: p.PolicySHA256, RepositoryID: observed.RepositoryID, CredentialReference: m.CredentialReference, CredentialVersion: m.Scope.CredentialVersion, CredentialReceiptID: m.CredentialReceiptID, RecoveryReference: m.RecoveryReference, RecoveryVersion: m.Scope.RecoveryVersion, RecoveryDeliveryID: deliveryID, RecoverySHA256: m.RecoverySHA256, Backup: p.Backup, UnitSHA256: observed.UnitSHA256, TimerSHA256: observed.TimerSHA256, ActivatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(policy)
	var stage string
	if err := tx.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_revision_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", a.operationID).Scan(&stage); err != nil || stage != "units_issued" {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	for _, next := range []string{"units_installed", "verified", "committed"} {
		if err := appendNativeBackupEvent(ctx, tx, a, next, raw); err != nil {
			return nativecapture.BackupPolicyReceipt{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO data_native_backup_policies VALUES(?,?,?,?)", p.BindingID, policy.Revision, p.EpochID, raw); err != nil {
		return nativecapture.BackupPolicyReceipt{}, err
	}
	if !a.active() {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	receipt := nativecapture.BackupPolicyReceipt{Kind: string(ops.NativeBackupActivate), OperationID: a.operationID, BindingID: string(p.BindingID), EpochID: string(p.EpochID), PlanID: p.ID, Revision: policy.Revision, PolicySHA256: nativePointHash(raw), RepositoryID: observed.RepositoryID, Outcome: "active"}
	return receipt, tx.Commit()
}
