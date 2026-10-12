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
	inspection  *NativeBackupInspection
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
	if err = refusePendingNativeBackupActivation(ctx, tx, current.BindingID, operationID); err != nil {
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

func refusePendingNativeBackupActivation(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, operationID string) error {
	rows, err := q.QueryContext(ctx, "SELECT o.id FROM data_native_backup_revision_claims c JOIN data_native_backup_revision_plans p ON p.id=c.plan_id JOIN operations o ON o.id=c.operation_id WHERE p.binding_id=? AND c.operation_id<>? AND o.state<>'succeeded'", binding, operationID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range ids {
		original, err := nativeResolutionOperation(ctx, q, id)
		if err != nil {
			return err
		}
		if _, err = readNativeBackupResolvedReceipt(ctx, q, original); err != nil {
			return ErrConflict
		}
	}
	return nil
}

func nativeBackupActivationMetadataTx(ctx context.Context, tx *sql.Tx, a *NativeBackupActivation) (NativeBackupActivationMetadata, error) {
	if !a.active() {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	var plan NativeBackupRevisionPlan
	var err error
	if a.inspection != nil {
		err = a.inspection.check(ctx, tx)
		plan = a.plan
	} else {
		plan, err = readNativeBackupRevisionPlan(ctx, tx, a.plan.ID)
	}
	if err != nil {
		return NativeBackupActivationMetadata{}, err
	}
	var claim []byte
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_revision_claims WHERE plan_id=? AND operation_id=?", plan.ID, a.operationID).Scan(&claim)
	op, opErr := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", a.operationID))
	if err != nil || !bytes.Equal(claim, a.claim) || opErr != nil || op.Kind != ops.NativeBackupActivate || (a.inspection == nil && op.State != ops.Preflight || a.inspection != nil && op.State != ops.RecoveryRequired) || op.Requester != plan.Requester || op.SecretRef != plan.ID || op.App != plan.App {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	permit, err := readEnginePermit(ctx, tx, plan.BindingID)
	reserved := permit.Reserved
	if err != nil || reserved.Archive.EpochID != plan.EpochID {
		return NativeBackupActivationMetadata{}, errors.Join(ErrConflict, err)
	}
	previous, _, err := readNativeBackupPolicy(ctx, tx, plan.BindingID)
	if a.inspection != nil {
		previous, err = readNativeBackupPreviousPolicy(ctx, tx, plan)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return NativeBackupActivationMetadata{}, err
	}
	var inherited NativeRepositoryMaterial
	if previous.Revision != 0 {
		if a.inspection != nil && previous.EpochID == plan.EpochID {
			inherited.Scope.RepositoryID = previous.RepositoryID
		} else if a.inspection != nil {
			epoch, archive, epochErr := readNativeEpoch(ctx, tx, plan.BindingID, plan.EpochID)
			if epochErr != nil {
				return NativeBackupActivationMetadata{}, epochErr
			}
			scope := restic.Scope{BindingID: string(plan.BindingID), EpochID: string(plan.EpochID), RepositoryID: epoch.Record().RepositoryID, DestinationSHA256: nativeArchiveLocation(archive).SHA256(), CredentialVersion: previous.CredentialVersion, RecoveryVersion: previous.RecoveryVersion}
			inherited, err = readNativeInheritedRepositoryMaterial(ctx, tx, restic.TransportPoint{Scope: scope}, archive, map[string]bool{})
		} else {
			inherited, err = readNativeCurrentRepositoryMaterialRecords(ctx, tx, permit)
		}
		if err != nil {
			return NativeBackupActivationMetadata{}, err
		}
	}
	var credentialRaw, recoveryRaw []byte
	d := reserved.Archive.Destination
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE binding_id=? AND json_extract(canonical,'$.epoch_id')=? AND kind='receipt' AND json_extract(canonical,'$.credential_ref')=? ORDER BY json_extract(canonical,'$.version') DESC LIMIT 1", plan.BindingID, plan.EpochID, d.CredentialRef).Scan(&credentialRaw)
	if errors.Is(err, sql.ErrNoRows) && previous.Revision != 0 {
		credentialRaw, err = json.Marshal(inherited.CredentialRecord)
	}
	var credential CredentialRecord
	if err != nil || json.Unmarshal(credentialRaw, &credential) != nil {
		return NativeBackupActivationMetadata{}, errors.Join(ErrConflict, err)
	}
	canonical, _ := json.Marshal(credential)
	if !bytes.Equal(canonical, credentialRaw) || credential.BindingID != plan.BindingID || credential.EpochID != plan.EpochID && credential.ID != inherited.CredentialReceiptID || credential.CredentialRef != d.CredentialRef || credential.Version == 0 || credential.Kind != "receipt" || credential.ReceivedAt.IsZero() {
		return NativeBackupActivationMetadata{}, &IntegrityError{}
	}
	err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_recovery_deliveries WHERE binding_id=? AND epoch_id=? AND reference=? ORDER BY version DESC LIMIT 1", plan.BindingID, plan.EpochID, plan.RecoveryReference).Scan(&recoveryRaw)
	if errors.Is(err, sql.ErrNoRows) && previous.Revision != 0 {
		err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_recovery_deliveries WHERE id=?", inherited.RecoveryDeliveryID).Scan(&recoveryRaw)
	}
	var recovery nativeRecoveryDeliveryRecord
	if err != nil || json.Unmarshal(recoveryRaw, &recovery) != nil || recovery.BindingID != plan.BindingID || recovery.Reference != plan.RecoveryReference || checkNativeRecoveryMaterialOperation(ctx, tx, recovery) != nil {
		return NativeBackupActivationMetadata{}, errors.Join(ErrConflict, err)
	}
	canonical, _ = json.Marshal(recovery)
	if !bytes.Equal(canonical, recoveryRaw) || recovery.Version == 0 || !data.ValidCatalogHash(recovery.SHA256) || recovery.ReceivedAt.IsZero() {
		return NativeBackupActivationMetadata{}, &IntegrityError{}
	}
	if recovery.EpochID != plan.EpochID && (recovery.ID != inherited.RecoveryDeliveryID || recovery.Reference != inherited.RecoveryReference || recovery.Version != inherited.Scope.RecoveryVersion || recovery.SHA256 != inherited.RecoverySHA256) {
		return NativeBackupActivationMetadata{}, ErrConflict
	}
	location := restic.S3Location{Endpoint: d.Endpoint, Bucket: d.Bucket, Prefix: strings.TrimSuffix(reserved.Archive.RemotePrefix, "/"), Region: d.Region, PathStyle: d.PathStyle}
	scope := restic.Scope{BindingID: string(plan.BindingID), EpochID: string(plan.EpochID), RepositoryID: previous.RepositoryID, DestinationSHA256: location.SHA256(), CredentialVersion: credential.Version, RecoveryVersion: recovery.Version, ExpiresAt: plan.ExpiresAt}
	if previous.Revision != 0 {
		scope.RepositoryID = inherited.Scope.RepositoryID
	}
	if a.inspection != nil {
		scope.ExpiresAt = a.inspection.deadline
	}
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
	if err := tx.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_initialization_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", a.operationID).Scan(&stage); err != nil {
		return ErrConflict
	}
	if a.inspection != nil && stage == "initialized" {
		var raw []byte
		var original nativeBackupInitEvent
		err = tx.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_initialization_events WHERE operation_id=? AND stage='initialized'", a.operationID).Scan(&raw)
		if err != nil || json.Unmarshal(raw, &original) != nil || original.RepositoryID != proof.Scope().RepositoryID || original.ConfigSHA256 != proof.ConfigurationSHA256() {
			return ErrConflict
		}
		return tx.Commit()
	}
	if stage != "unknown" {
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
		if err != nil || json.Unmarshal(raw, &initialized) != nil || initialized.RepositoryID != observed.RepositoryID || (!sameNativeResolutionScope(initialized.Scope, metadata.Material.Scope) || a.inspection == nil && initialized.Scope.ExpiresAt != metadata.Material.Scope.ExpiresAt) || !data.ValidCatalogHash(initialized.ConfigSHA256) {
			return nativecapture.BackupPolicyReceipt{}, ErrConflict
		}
	} else if metadata.Material.Scope.RepositoryID != observed.RepositoryID {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	var deliveryID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM data_native_recovery_deliveries WHERE id=? AND binding_id=? AND reference=? AND version=?", metadata.Material.RecoveryDeliveryID, metadata.Plan.BindingID, metadata.Material.RecoveryReference, metadata.Material.Scope.RecoveryVersion).Scan(&deliveryID)
	if err != nil || deliveryID != observed.RecoveryDeliveryID {
		return nativecapture.BackupPolicyReceipt{}, ErrConflict
	}
	p, m := metadata.Plan, metadata.Material
	if a.inspection != nil {
		existing, existingErr := readNativeBackupInstalledPolicy(ctx, tx, p)
		if existingErr == nil {
			if existing.RepositoryID != observed.RepositoryID || existing.UnitSHA256 != observed.UnitSHA256 || existing.TimerSHA256 != observed.TimerSHA256 || existing.RecoveryDeliveryID != observed.RecoveryDeliveryID {
				return nativecapture.BackupPolicyReceipt{}, ErrConflict
			}
			raw, _ := json.Marshal(existing)
			return nativeBackupPolicyReceipt(a.operationID, p, nativePointHash(raw), existing), tx.Commit()
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return nativecapture.BackupPolicyReceipt{}, existingErr
		}
	}
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
