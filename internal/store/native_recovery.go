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
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/policy"
)

type NativeRecoveryInput struct {
	ID               string                `json:"id"`
	DeploymentPlanID string                `json:"deployment_plan_id"`
	Requester        string                `json:"requester"`
	IdempotencyKey   string                `json:"idempotency_key"`
	App              string                `json:"app"`
	BindingID        data.ReplicaBindingID `json:"binding_id"`
	EpochID          data.ReplicaEpochID   `json:"epoch_id"`
	Reference        string                `json:"reference"`
	Version          uint64                `json:"version"`
	MaterialSHA256   string                `json:"material_sha256"`
	RuntimeVersion   uint64                `json:"runtime_version"`
	RuntimeSHA256    string                `json:"runtime_sha256"`
	PolicySHA256     string                `json:"policy_sha256"`
	CreatedAt        time.Time             `json:"created_at"`
}

func (s *Store) ReserveNativeRecoveryInput(ctx context.Context, planID, requester, key, materialSHA string) (NativeRecoveryInput, error) {
	identity, err := datainit.ParseRequester(requester)
	if s.readOnly || err != nil || !identity.Valid() || !data.ValidID(key) || !data.ValidCatalogHash(materialSHA) {
		return NativeRecoveryInput{}, ErrInvalid
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return NativeRecoveryInput{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM data_native_recovery_inputs WHERE requester=? AND json_extract(canonical,'$.idempotency_key')=?", requester, key).Scan(&existingID)
	if err == nil {
		existing, err := readNativeRecoveryInput(ctx, tx, existingID)
		if err != nil || existing.DeploymentPlanID != planID || existing.MaterialSHA256 != materialSHA {
			return NativeRecoveryInput{}, errors.Join(ErrConflict, err)
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return NativeRecoveryInput{}, err
	}
	p, desired, err := loadPlan(ctx, tx, planID)
	if err != nil || p.Managed == nil || policy.ValidateManagedDesired(desired) != nil {
		return NativeRecoveryInput{}, errors.Join(ErrConflict, err)
	}
	head, _, err := readEngineHead(ctx, tx, p.Managed.Database.BindingID)
	if err != nil || head.EpochID != p.Managed.Archive.EpochID {
		return NativeRecoveryInput{}, errors.Join(ErrConflict, err)
	}
	id, err := data.NewID()
	if err != nil {
		return NativeRecoveryInput{}, err
	}
	input := NativeRecoveryInput{ID: id, DeploymentPlanID: planID, Requester: requester, IdempotencyKey: key, App: p.App, BindingID: head.BindingID, EpochID: head.EpochID, Reference: string(desired.SpacetimeDB.RecoveryPasswordRef), MaterialSHA256: materialSHA, RuntimeVersion: head.Version, RuntimeSHA256: head.RuntimeSHA256, PolicySHA256: strings.TrimPrefix(desired.PolicyHash, "sha256:"), CreatedAt: time.Now().UTC()}
	if err := checkNativeRecoveryInputCurrent(ctx, tx, input); err != nil {
		return NativeRecoveryInput{}, err
	}
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0)+1 FROM data_native_recovery_inputs WHERE binding_id=? AND reference=?", head.BindingID, input.Reference).Scan(&input.Version)
	if err != nil || input.Version == 0 || input.Version > 1<<63-1 {
		return NativeRecoveryInput{}, errors.Join(ErrConflict, err)
	}
	raw, _ := json.Marshal(input)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_recovery_inputs VALUES(?,?,?,?,?,?,?,?,?,?)", id, planID, requester, input.App, head.BindingID, head.EpochID, input.Reference, input.Version, materialSHA, raw)
	if err != nil {
		return NativeRecoveryInput{}, err
	}
	return input, tx.Commit()
}

func readNativeRecoveryInput(ctx context.Context, q dataQuerier, id string) (NativeRecoveryInput, error) {
	var v NativeRecoveryInput
	var planID, requester, app, reference, hash string
	var binding data.ReplicaBindingID
	var epoch data.ReplicaEpochID
	var version uint64
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT deployment_plan_id,requester,app,binding_id,epoch_id,reference,version,material_sha256,canonical FROM data_native_recovery_inputs WHERE id=?", id).Scan(&planID, &requester, &app, &binding, &epoch, &reference, &version, &hash, &raw)
	if err != nil || len(raw) == 0 || len(raw) > 8192 || json.Unmarshal(raw, &v) != nil {
		return v, errors.Join(&IntegrityError{}, err)
	}
	canonical, _ := json.Marshal(v)
	if !bytes.Equal(raw, canonical) || v.ID != id || !data.ValidID(id) || v.DeploymentPlanID != planID || v.Requester != requester || v.App != app || v.BindingID != binding || v.EpochID != epoch || v.Reference != reference || v.Version != version || v.MaterialSHA256 != hash || !data.ValidID(v.IdempotencyKey) || !data.ValidCatalogHash(hash) || v.CreatedAt.IsZero() || v.CreatedAt.Location() != time.UTC {
		return v, &IntegrityError{}
	}
	return v, checkNativeRecoveryInputCurrent(ctx, q, v)
}

func checkNativeRecoveryInputCurrent(ctx context.Context, q dataQuerier, input NativeRecoveryInput) error {
	p, desired, err := loadPlan(ctx, q, input.DeploymentPlanID)
	if err != nil || p.Managed == nil || policy.ValidateManagedDesired(desired) != nil || p.App != input.App || p.Managed.Database.BindingID != input.BindingID || p.Managed.Archive.EpochID != input.EpochID || string(desired.SpacetimeDB.RecoveryPasswordRef) != input.Reference || strings.TrimPrefix(desired.PolicyHash, "sha256:") != input.PolicySHA256 {
		return errors.Join(ErrConflict, err)
	}
	head, runtime, err := readEngineHead(ctx, q, input.BindingID)
	if err != nil || head.EpochID != input.EpochID || head.Version != input.RuntimeVersion || head.RuntimeSHA256 != input.RuntimeSHA256 || runtime.Secrets.Publisher.Reference == input.Reference || runtime.Secrets.SigningPrivate.Reference == input.Reference || runtime.Secrets.SigningPublic.Reference == input.Reference || runtime.Secrets.AuthPolicy.Reference == input.Reference || desired.BackupDestinations[0].CredentialRef == input.Reference {
		return errors.Join(ErrConflict, err)
	}
	for _, material := range []string{runtime.Secrets.Publisher.SHA256, runtime.Secrets.SigningPrivate.SHA256, runtime.Secrets.SigningPublic.SHA256, runtime.Secrets.AuthPolicy.SHA256} {
		if input.MaterialSHA256 == material {
			return ErrConflict
		}
	}
	var admission string
	err = q.QueryRowContext(ctx, "SELECT policy_hash FROM data_admissions WHERE database_id=? ORDER BY version DESC LIMIT 1", p.Managed.Database.DatabaseID).Scan(&admission)
	if err != nil || strings.TrimPrefix(admission, "sha256:") != input.PolicySHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

type NativeRecoveryDelivery struct {
	state *Store
	input NativeRecoveryInput
	guard *data.EngineLock
}

func (s *Store) ClaimNativeRecoveryDelivery(ctx context.Context, id, requester string) (*NativeRecoveryDelivery, error) {
	input, err := readNativeRecoveryInput(ctx, s.db, id)
	if s.readOnly || err != nil || input.Requester != requester {
		return nil, errors.Join(ErrConflict, err)
	}
	guard, err := data.AcquireEngineLaunchGuard(ctx, s.dir, input.BindingID)
	if err != nil {
		return nil, err
	}
	capability := &NativeRecoveryDelivery{state: s, input: input, guard: guard}
	if _, err := capability.Input(ctx); err != nil {
		_ = guard.Release()
		return nil, err
	}
	return capability, nil
}

func (r *NativeRecoveryDelivery) Input(ctx context.Context) (NativeRecoveryInput, error) {
	if r == nil || r.state == nil || !r.guard.HoldsLaunch(r.state.dir, r.input.BindingID) {
		return NativeRecoveryInput{}, ErrConflict
	}
	input, err := readNativeRecoveryInput(ctx, r.state.db, r.input.ID)
	before, _ := json.Marshal(r.input)
	after, _ := json.Marshal(input)
	if err != nil || !bytes.Equal(before, after) {
		return NativeRecoveryInput{}, errors.Join(ErrConflict, err)
	}
	return input, nil
}
func (r *NativeRecoveryDelivery) StateRoot() string {
	if r == nil || r.state == nil {
		return ""
	}
	return r.state.dir
}
func (r *NativeRecoveryDelivery) Close() error {
	if r == nil || r.guard == nil {
		return nil
	}
	return r.guard.Release()
}

func (s *Store) RecordNativeRecoveryDelivery(ctx context.Context, r *NativeRecoveryDelivery, operationID string) (nativecapture.RecoveryReceipt, error) {
	if s.readOnly || r == nil || r.state != s {
		return nativecapture.RecoveryReceipt{}, ErrConflict
	}
	input, err := r.Input(ctx)
	if err != nil {
		return nativecapture.RecoveryReceipt{}, err
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nativecapture.RecoveryReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	op, err := scanOperation(tx.QueryRowContext(ctx, "SELECT id,COALESCE(plan_id,''),requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref,recovery_of FROM operations WHERE id=?", operationID))
	if err != nil || op.Kind != ops.NativeRecoverySet || op.State != ops.Preflight || op.Requester != input.Requester || op.App != input.App || op.SecretRef != input.ID || op.IdempotencyKey != input.IdempotencyKey || checkNativeRecoveryInputCurrent(ctx, tx, input) != nil {
		return nativecapture.RecoveryReceipt{}, errors.Join(ErrConflict, err)
	}
	id, err := data.NewID()
	if err != nil {
		return nativecapture.RecoveryReceipt{}, err
	}
	record := nativeRecoveryDeliveryRecord{Format: "brine-native-recovery-delivery-v1", ID: id, OperationID: operationID, Requester: input.Requester, BindingID: input.BindingID, EpochID: input.EpochID, Reference: input.Reference, Version: input.Version, SHA256: input.MaterialSHA256, ReceivedAt: time.Now().UTC()}
	raw, _ := json.Marshal(record)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_recovery_deliveries VALUES(?,?,?,?,?,?,?,?)", id, operationID, input.Requester, input.BindingID, input.EpochID, input.Reference, input.Version, raw)
	if err != nil || !r.guard.HoldsLaunch(s.dir, input.BindingID) {
		return nativecapture.RecoveryReceipt{}, errors.Join(ErrConflict, err)
	}
	receipt := nativecapture.RecoveryReceipt{Kind: string(ops.NativeRecoverySet), OperationID: operationID, BindingID: string(input.BindingID), EpochID: string(input.EpochID), Reference: input.Reference, Version: input.Version, SHA256: input.MaterialSHA256, Outcome: "stored"}
	return receipt, tx.Commit()
}
