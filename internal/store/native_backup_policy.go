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
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type NativeBackupPolicy struct {
	Format              string                 `json:"format"`
	BindingID           data.ReplicaBindingID  `json:"binding_id"`
	EpochID             data.ReplicaEpochID    `json:"epoch_id"`
	Revision            uint64                 `json:"revision"`
	OperationID         string                 `json:"operation_id"`
	RevisionPlanID      string                 `json:"revision_plan_id"`
	DeploymentPlanID    string                 `json:"deployment_plan_id"`
	DesiredSHA256       string                 `json:"desired_sha256"`
	PolicySHA256        string                 `json:"policy_sha256"`
	RepositoryID        string                 `json:"repository_id"`
	CredentialReference string                 `json:"credential_reference"`
	CredentialVersion   uint64                 `json:"credential_version"`
	CredentialReceiptID string                 `json:"credential_receipt_id"`
	RecoveryReference   string                 `json:"recovery_reference"`
	RecoveryVersion     uint64                 `json:"recovery_version"`
	RecoveryDeliveryID  string                 `json:"recovery_delivery_id"`
	RecoverySHA256      string                 `json:"recovery_sha256"`
	Backup              policy.SpacetimePolicy `json:"backup"`
	UnitSHA256          string                 `json:"unit_sha256"`
	TimerSHA256         string                 `json:"timer_sha256"`
	ActivatedAt         time.Time              `json:"activated_at"`
}

type NativeBackupRevisionPlan struct {
	Format            string                 `json:"format"`
	ID                string                 `json:"id"`
	DeploymentPlanID  string                 `json:"deployment_plan_id"`
	Requester         string                 `json:"requester"`
	App               string                 `json:"app"`
	BindingID         data.ReplicaBindingID  `json:"binding_id"`
	EpochID           data.ReplicaEpochID    `json:"epoch_id"`
	RuntimeVersion    uint64                 `json:"runtime_version"`
	RuntimeSHA256     string                 `json:"runtime_sha256"`
	PreviousRevision  uint64                 `json:"previous_revision"`
	PreviousSHA256    string                 `json:"previous_sha256"`
	DesiredSHA256     string                 `json:"desired_sha256"`
	PolicySHA256      string                 `json:"policy_sha256"`
	RecoveryReference string                 `json:"recovery_reference"`
	Backup            policy.SpacetimePolicy `json:"backup"`
	Retention         data.RetentionEvidence `json:"retention"`
	CreatedAt         time.Time              `json:"created_at"`
	ExpiresAt         time.Time              `json:"expires_at"`
}

// SaveNativeBackupRevisionPlan derives the candidate from a previously saved
// managed plan. Callers supply no policy, destination or material version body.
func (s *Store) SaveNativeBackupRevisionPlan(ctx context.Context, deploymentPlanID PlanID, requester string) (NativeBackupRevisionPlan, error) {
	identity, err := datainit.ParseRequester(requester)
	if s.readOnly || err != nil || !identity.Valid() {
		return NativeBackupRevisionPlan{}, ErrInvalid
	}
	p, desired, err := s.LoadPlan(ctx, deploymentPlanID)
	if err != nil || p.Managed == nil || policy.ValidateManagedDesired(desired) != nil {
		return NativeBackupRevisionPlan{}, errors.Join(ErrConflict, err)
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, p.Managed.Database.BindingID)
	if err != nil {
		return NativeBackupRevisionPlan{}, err
	}
	defer func() { _ = guard.Release() }()
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return NativeBackupRevisionPlan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	p, desired, err = loadPlan(ctx, tx, deploymentPlanID)
	if err != nil {
		return NativeBackupRevisionPlan{}, err
	}
	candidate, err := buildNativeBackupRevisionPlan(ctx, tx, p, desired, deploymentPlanID, requester)
	if err != nil {
		return NativeBackupRevisionPlan{}, err
	}
	id, err := data.NewID()
	if err != nil {
		return NativeBackupRevisionPlan{}, err
	}
	now := time.Now().UTC()
	candidate.ID, candidate.CreatedAt, candidate.ExpiresAt = id, now, now.Add(15*time.Minute)
	raw, err := json.Marshal(candidate)
	if err != nil || len(raw) > 65536 {
		return NativeBackupRevisionPlan{}, ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_revision_plans VALUES(?,?,?,?,?)", id, requester, candidate.BindingID, candidate.EpochID, raw)
	if err != nil || !guard.HoldsLaunch(s.dir, candidate.BindingID) {
		return NativeBackupRevisionPlan{}, errors.Join(ErrConflict, err)
	}
	return candidate, tx.Commit()
}

func buildNativeBackupRevisionPlan(ctx context.Context, q dataQuerier, p plan.Plan, desired policy.Desired, planID, requester string) (NativeBackupRevisionPlan, error) {
	if p.Managed == nil || policy.ValidateManagedDesired(desired) != nil || p.App != string(desired.Name) {
		return NativeBackupRevisionPlan{}, ErrInvalid
	}
	for _, conflict := range p.Conflicts {
		if conflict.Code != plan.ManagedBackupRevisionRequired && (p.Managed.State != data.EngineUnpublished || conflict.Code != plan.ManagedPublicationRequired) {
			return NativeBackupRevisionPlan{}, ErrConflict
		}
	}
	head, runtime, err := readEngineHead(ctx, q, p.Managed.Database.BindingID)
	if err != nil || head.EpochID != p.Managed.Archive.EpochID || p.Managed.RuntimeSHA256 != head.RuntimeSHA256 && (runtime.State != data.EngineUnpublished || p.Managed.RuntimeSHA256 != "") || runtime.State != p.Managed.State {
		return NativeBackupRevisionPlan{}, errors.Join(ErrConflict, err)
	}
	reserved, err := readReservedEngine(ctx, q, head.BindingID)
	if err != nil || reserved.Database != p.Managed.Database || desired.BackupDestinations[0] != p.Managed.Archive.Destination {
		return NativeBackupRevisionPlan{}, errors.Join(ErrConflict, err)
	}
	if runtime.State == data.EngineInitialized {
		if err := checkNativeBackupOnlyPlan(ctx, q, p.App, desired); err != nil {
			return NativeBackupRevisionPlan{}, err
		}
	}
	if len(desired.BackupRetention) != 1 || !desired.BackupRetention[0].Admits(p.Managed.Archive.Destination, time.Now().UTC()) {
		return NativeBackupRevisionPlan{}, ErrConflict
	}
	previous, raw, err := readNativeBackupPolicy(ctx, q, head.BindingID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return NativeBackupRevisionPlan{}, err
	}
	previousHash := ""
	if err == nil {
		if previous.EpochID != head.EpochID {
			return NativeBackupRevisionPlan{}, ErrConflict
		}
		previousHash = nativePointHash(raw)
	}
	return NativeBackupRevisionPlan{Format: "brine-native-backup-revision-plan-v1", DeploymentPlanID: planID, Requester: requester, App: p.App, BindingID: head.BindingID, EpochID: head.EpochID, RuntimeVersion: head.Version, RuntimeSHA256: head.RuntimeSHA256, PreviousRevision: previous.Revision, PreviousSHA256: previousHash, DesiredSHA256: strings.TrimPrefix(p.DesiredHash, "sha256:"), PolicySHA256: strings.TrimPrefix(desired.PolicyHash, "sha256:"), RecoveryReference: string(desired.SpacetimeDB.RecoveryPasswordRef), Backup: *desired.SpacetimePolicy, Retention: desired.BackupRetention[0]}, nil
}

func checkNativeBackupOnlyPlan(ctx context.Context, q dataQuerier, app string, desired policy.Desired) error {
	var previousPlan string
	err := q.QueryRowContext(ctx, "SELECT r.plan_id FROM release_heads h JOIN releases r ON r.app=h.app AND r.id=h.current_id WHERE h.app=?", app).Scan(&previousPlan)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	_, previous, err := loadPlan(ctx, q, previousPlan)
	if err != nil || !plan.ManagedBackupOnly(desired, previous) {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func readNativeBackupPolicy(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID) (NativeBackupPolicy, []byte, error) {
	return readNativeBackupPolicyScope(ctx, q, binding, "", "", 0, 0)
}

func readNativeBackupPolicyScope(ctx context.Context, q dataQuerier, binding data.ReplicaBindingID, selectedEpoch data.ReplicaEpochID, repository string, credentialVersion, recoveryVersion uint64) (NativeBackupPolicy, []byte, error) {
	var value NativeBackupPolicy
	var revision uint64
	var epoch data.ReplicaEpochID
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT revision,epoch_id,canonical FROM data_native_backup_policies WHERE binding_id=? AND (?='' OR epoch_id=?) AND (?='' OR json_extract(canonical,'$.repository_id')=?) AND (?=0 OR json_extract(canonical,'$.credential_version')=?) AND (?=0 OR json_extract(canonical,'$.recovery_version')=?) ORDER BY revision DESC LIMIT 1", binding, selectedEpoch, selectedEpoch, repository, repository, credentialVersion, credentialVersion, recoveryVersion, recoveryVersion).Scan(&revision, &epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil, ErrNotFound
	}
	if err != nil {
		return value, nil, err
	}
	if len(raw) == 0 || len(raw) > 16384 || json.Unmarshal(raw, &value) != nil {
		return value, nil, &IntegrityError{}
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(raw, canonical) || value.Format != "brine-native-backup-policy-v1" || value.BindingID != binding || value.EpochID != epoch || value.Revision != revision || !value.Backup.Valid() || value.CredentialVersion == 0 || value.RecoveryVersion == 0 || !nativePointLogicalName.MatchString(value.CredentialReference) || !nativePointLogicalName.MatchString(value.RecoveryReference) || !data.ValidID(value.RevisionPlanID) || !data.ValidID(value.CredentialReceiptID) || !data.ValidID(value.RecoveryDeliveryID) || !nativePointOperationID.MatchString(value.OperationID) || value.ActivatedAt.IsZero() || value.ActivatedAt.Location() != time.UTC {
		return value, nil, &IntegrityError{}
	}
	for _, hash := range []string{value.DesiredSHA256, value.PolicySHA256, value.RepositoryID, value.RecoverySHA256, value.UnitSHA256, value.TimerSHA256} {
		if !data.ValidCatalogHash(hash) {
			return value, nil, &IntegrityError{}
		}
	}
	return value, bytes.Clone(raw), nil
}

func (s *Store) ReadNativeBackupPolicy(ctx context.Context, binding data.ReplicaBindingID) (NativeBackupPolicy, error) {
	value, _, err := readNativeBackupPolicy(ctx, s.db, binding)
	return value, err
}

func (s *Store) ReadNativeBackupRevisionPlan(ctx context.Context, id string) (NativeBackupRevisionPlan, error) {
	return readNativeBackupRevisionPlan(ctx, s.db, id)
}

func readNativeBackupRevisionPlan(ctx context.Context, q dataQuerier, id string) (NativeBackupRevisionPlan, error) {
	var value NativeBackupRevisionPlan
	var requester string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT requester,binding_id,epoch_id,canonical FROM data_native_backup_revision_plans WHERE id=?", id).Scan(&requester, &binding, &epoch, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, err
	}
	fields := []string{"format", "id", "deployment_plan_id", "requester", "app", "binding_id", "epoch_id", "runtime_version", "runtime_sha256", "previous_revision", "previous_sha256", "desired_sha256", "policy_sha256", "recovery_reference", "backup", "retention", "created_at", "expires_at"}
	if len(raw) == 0 || len(raw) > 65536 {
		return value, &IntegrityError{}
	}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &value) != nil {
		return value, &IntegrityError{}
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(raw, canonical) || value.Format != "brine-native-backup-revision-plan-v1" || value.ID != id || !data.ValidID(id) || value.Requester != requester || value.BindingID != binding || value.EpochID != epoch {
		return value, &IntegrityError{}
	}
	p, desired, err := loadPlan(ctx, q, value.DeploymentPlanID)
	if err != nil {
		return value, err
	}
	expected, err := buildNativeBackupRevisionPlan(ctx, q, p, desired, value.DeploymentPlanID, requester)
	expected.ID, expected.CreatedAt, expected.ExpiresAt = value.ID, value.CreatedAt, value.ExpiresAt
	expectedRaw, marshalErr := json.Marshal(expected)
	if err != nil || marshalErr != nil || !bytes.Equal(expectedRaw, raw) || value.CreatedAt.IsZero() || value.CreatedAt.Location() != time.UTC || value.ExpiresAt.Location() != time.UTC || !value.ExpiresAt.Equal(value.CreatedAt.Add(15*time.Minute)) || !time.Now().UTC().Before(value.ExpiresAt) {
		return value, errors.Join(ErrConflict, err)
	}
	return value, nil
}
