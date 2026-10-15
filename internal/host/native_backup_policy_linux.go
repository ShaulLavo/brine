//go:build linux

package host

import (
	"context"

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
	if err != nil || policyErr != nil {
		return store.NativeBackupPolicy{}, result.New(result.PolicyRefused, nil)
	}
	deployment, desired, err := service.Store.LoadPlan(ctx, backup.DeploymentPlanID)
	if err != nil || deployment.Managed == nil || deployment.Managed.Database.BindingID != binding || deployment.DesiredHash != "sha256:"+backup.DesiredSHA256 || desired.PolicyHash != "sha256:"+backup.PolicySHA256 || desired.SpacetimePolicy == nil || *desired.SpacetimePolicy != backup.Backup {
		return store.NativeBackupPolicy{}, result.New(result.PolicyRefused, nil)
	}
	if err := pol.CheckNativeBackup(desired); err != nil {
		return store.NativeBackupPolicy{}, err
	}
	return backup, nil
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
