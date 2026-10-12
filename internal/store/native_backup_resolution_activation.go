package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/restic"
)

func sameNativeResolutionScope(a, b restic.Scope) bool { a.ExpiresAt = b.ExpiresAt; return a == b }

func (i *NativeBackupInspection) Activation(ctx context.Context) (*NativeBackupActivation, error) {
	if i.original.Kind != ops.NativeBackupActivate {
		return nil, ErrConflict
	}
	if err := i.check(ctx, i.state.db); err != nil {
		return nil, err
	}
	if i.activation != nil {
		return i.activation, nil
	}
	var raw, claim []byte
	var p NativeBackupRevisionPlan
	err := i.state.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_revision_plans WHERE id=?", i.original.SecretRef).Scan(&raw)
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &p) != nil {
		return nil, ErrConflict
	}
	canonical, _ := json.Marshal(p)
	if !bytes.Equal(raw, canonical) || p.ID != i.original.SecretRef || p.Requester != i.original.Requester || p.BindingID != i.binding || p.App != i.original.App {
		return nil, &IntegrityError{}
	}
	var c nativeBackupClaim
	err = i.state.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_revision_claims WHERE operation_id=? AND plan_id=?", i.original.ID, p.ID).Scan(&claim)
	if err != nil || json.Unmarshal(claim, &c) != nil || c.OperationID != i.original.ID || c.PlanID != p.ID || c.PlanSHA256 != nativePointHash(raw) {
		return nil, ErrConflict
	}
	canonical, _ = json.Marshal(c)
	if !bytes.Equal(canonical, claim) {
		return nil, &IntegrityError{}
	}
	head, runtime, err := readEngineHead(ctx, i.state.db, i.binding)
	runtimeRaw, _ := json.Marshal(runtime)
	if err != nil || head.Version != p.RuntimeVersion || nativePointHash(runtimeRaw) != p.RuntimeSHA256 || runtime.EpochID != p.EpochID {
		return nil, ErrConflict
	}
	i.activation = &NativeBackupActivation{state: i.state, guard: i.guard, operationID: i.original.ID, plan: p, claim: claim, inspection: i}
	return i.activation, nil
}
func readNativeBackupPreviousPolicy(ctx context.Context, q dataQuerier, p NativeBackupRevisionPlan) (NativeBackupPolicy, error) {
	if p.PreviousRevision == 0 {
		return NativeBackupPolicy{}, ErrNotFound
	}
	var raw []byte
	var value NativeBackupPolicy
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_policies WHERE binding_id=? AND revision=? AND epoch_id=?", p.BindingID, p.PreviousRevision, p.EpochID).Scan(&raw)
	if err != nil {
		return value, err
	}
	canonicalErr := json.Unmarshal(raw, &value)
	canonical, _ := json.Marshal(value)
	if canonicalErr != nil || !bytes.Equal(raw, canonical) || nativePointHash(raw) != p.PreviousSHA256 {
		return value, &IntegrityError{}
	}
	return value, nil
}
func readNativeBackupInstalledPolicy(ctx context.Context, q dataQuerier, p NativeBackupRevisionPlan) (NativeBackupPolicy, error) {
	var raw []byte
	var value NativeBackupPolicy
	err := q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_policies WHERE binding_id=? AND revision=? AND epoch_id=?", p.BindingID, p.PreviousRevision+1, p.EpochID).Scan(&raw)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(raw, &value)
	canonical, _ := json.Marshal(value)
	if err != nil || !bytes.Equal(raw, canonical) || value.RevisionPlanID != p.ID || value.BindingID != p.BindingID || value.EpochID != p.EpochID || value.PolicySHA256 != p.PolicySHA256 || value.DeploymentPlanID != p.DeploymentPlanID || value.Backup != p.Backup {
		return value, &IntegrityError{}
	}
	return value, nil
}
func nativeBackupPolicyReceipt(op string, p NativeBackupRevisionPlan, hash string, v NativeBackupPolicy) nativecapture.BackupPolicyReceipt {
	return nativecapture.BackupPolicyReceipt{Kind: string(ops.NativeBackupActivate), OperationID: op, BindingID: string(p.BindingID), EpochID: string(p.EpochID), PlanID: p.ID, Revision: v.Revision, PolicySHA256: hash, RepositoryID: v.RepositoryID, Outcome: "active"}
}
func checkNativeResolvedProducer(ctx context.Context, q dataQuerier, original ops.Operation, receipt json.RawMessage) error {
	if original.Kind == ops.NativeBackupActivate {
		r, err := nativecapture.DecodeBackupPolicyReceipt(receipt)
		var raw []byte
		var p NativeBackupPolicy
		if err != nil {
			return err
		}
		err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_policies WHERE binding_id=? AND epoch_id=? AND revision=?", r.BindingID, r.EpochID, r.Revision).Scan(&raw)
		if err != nil || json.Unmarshal(raw, &p) != nil || p.OperationID != original.ID || p.RevisionPlanID != original.SecretRef || nativePointHash(raw) != r.PolicySHA256 || p.RepositoryID != r.RepositoryID {
			return ErrConflict
		}
		return nil
	}
	r, err := nativecapture.DecodeTaskReceipt(receipt)
	if err != nil {
		return err
	}
	var raw []byte
	if original.Kind == ops.NativeCapture {
		err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_verified_points WHERE id=? AND operation_id=? AND binding_id=? AND epoch_id=?", r.PointID, original.ID, r.BindingID, r.EpochID).Scan(&raw)
	} else {
		err = q.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_points WHERE id=? AND binding_id=? AND epoch_id=? AND json_extract(canonical,'$.preparation_operation_id')=?", r.PointID, r.BindingID, r.EpochID, original.ID).Scan(&raw)
	}
	if err != nil || nativePointHash(raw) != r.PointSHA256 {
		return errors.Join(ErrConflict, err)
	}
	return nil
}

func (i *NativeBackupInspection) ActivationStage(ctx context.Context) (string, error) {
	if i.original.Kind != ops.NativeBackupActivate {
		return "", ErrConflict
	}
	if err := i.check(ctx, i.state.db); err != nil {
		return "", err
	}
	a, err := i.Activation(ctx)
	if err != nil {
		return "", err
	}
	if a.plan.PreviousRevision == 0 {
		var initStage string
		err = i.state.db.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_initialization_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", i.original.ID).Scan(&initStage)
		if err != nil || initStage != "unknown" && initStage != "initialized" {
			return "", ErrConflict
		}
	}
	var stage string
	err = i.state.db.QueryRowContext(ctx, "SELECT stage FROM data_native_backup_revision_events WHERE operation_id=? ORDER BY sequence DESC LIMIT 1", i.original.ID).Scan(&stage)
	if err != nil || stage != "prepared" && stage != "units_issued" && stage != "committed" {
		return "", ErrConflict
	}
	return stage, nil
}
func (i *NativeBackupInspection) CheckReviewedObservation(ctx context.Context, hash string) error {
	if i.resolver == "" || hash != i.review.ObservationSHA256 {
		return ErrConflict
	}
	return i.check(ctx, i.state.db)
}
func (i *NativeBackupInspection) BeginResolutionUnits(ctx context.Context) error {
	if i.resolver == "" || i.original.Kind != ops.NativeBackupActivate {
		return ErrConflict
	}
	tx, cancel, err := i.state.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = i.check(ctx, tx); err != nil {
		return err
	}
	raw, _ := json.Marshal(i.review)
	_, err = tx.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,'units_issued',?)", i.resolver, raw)
	if err != nil {
		return err
	}
	return tx.Commit()
}
