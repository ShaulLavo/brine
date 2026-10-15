//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeBackupPolicyOperations struct {
	service Service
	jobs    jobs.Service
}

func admitNativeBackupPolicy(ctx context.Context, service Service, binding data.ReplicaBindingID) (store.NativeBackupPolicy, error) {
	backup, err := service.Store.ReadNativeBackupPolicy(ctx, binding)
	pol, policyErr := service.Policy.Load(ctx)
	if err != nil || policyErr != nil || pol.Hash() != "sha256:"+backup.PolicySHA256 {
		return store.NativeBackupPolicy{}, result.New(result.PolicyRefused, nil)
	}
	return backup, nil
}

func checkNativePointPolicy(ctx context.Context, service Service, expected string) error {
	current, err := service.Policy.Load(ctx)
	if err != nil || current.Hash() != "sha256:"+expected {
		return result.New(result.PolicyRefused, err)
	}
	return nil
}

func (o nativeBackupPolicyOperations) PlanPolicy(ctx context.Context, args dispatch.NativeBackupPolicyPlanArgs) (dispatch.NativeBackupPolicyPlanView, error) {
	if !jobs.ValidPlanID(args.DeploymentPlanID) {
		return dispatch.NativeBackupPolicyPlanView{}, result.New(result.InvalidUsage, nil)
	}
	p, desired, err := o.service.Store.LoadPlan(ctx, args.DeploymentPlanID)
	pol, policyErr := o.service.Policy.Load(ctx)
	if err != nil || policyErr != nil || p.Managed == nil || desired.PolicyHash != pol.Hash() {
		return dispatch.NativeBackupPolicyPlanView{}, result.New(result.PolicyRefused, nil)
	}
	plan, err := o.service.Store.SaveNativeBackupRevisionPlan(ctx, args.DeploymentPlanID, o.jobs.Requester)
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		return dispatch.NativeBackupPolicyPlanView{}, result.New(result.Conflict, nil)
	}
	if errors.Is(err, store.ErrInvalid) {
		return dispatch.NativeBackupPolicyPlanView{}, result.New(result.InvalidUsage, nil)
	}
	if err != nil {
		return dispatch.NativeBackupPolicyPlanView{}, err
	}
	return dispatch.NativeBackupPolicyPlanView{PlanID: plan.ID, BindingID: string(plan.BindingID), EpochID: string(plan.EpochID), PreviousRevision: plan.PreviousRevision, CaptureIntervalSeconds: plan.Backup.CaptureIntervalSeconds, VerificationIntervalSeconds: plan.Backup.VerificationIntervalSeconds, ExpiresAt: plan.ExpiresAt}, nil
}
func (o nativeBackupPolicyOperations) ActivatePolicy(ctx context.Context, args dispatch.NativeBackupPolicyActivateArgs) (jobs.Accepted, error) {
	plan, err := o.service.Store.ReadNativeBackupRevisionPlan(ctx, args.PlanID)
	pol, policyErr := o.service.Policy.Load(ctx)
	if err != nil || policyErr != nil || plan.Requester != o.jobs.Requester || pol.Hash() != "sha256:"+plan.PolicySHA256 {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeBackupActivate, App: plan.App, SecretRef: plan.ID}, args.IdempotencyKey)
}

func (o nativeBackupPolicyOperations) BackupStatus(ctx context.Context, args dispatch.NativeBackupStatusArgs) (nativecapture.BackupStatus, error) {
	return o.service.Store.NativeBackupStatus(ctx, args.App, args.Database)
}
