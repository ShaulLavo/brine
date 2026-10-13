//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicArchiveMaintenance struct {
	state       *store.Store
	maintenance archive.MaintenanceService
	credentials backupcredentials.ArchiveService
	jobs        jobs.Service
}

func (o publicArchiveMaintenance) PlanMaintenance(ctx context.Context, a dispatch.ArchiveMaintenancePlanArgs) (archive.MaintenancePlan, error) {
	return o.maintenance.Plan(ctx, a.ArchiveID, a.Kind)
}
func (o publicArchiveMaintenance) ArchiveStatus(ctx context.Context, a dispatch.ArchiveMaintenanceStatusArgs) (archive.MaintenanceStatus, error) {
	return o.maintenance.Status(ctx, a.ArchiveID)
}
func (o publicArchiveMaintenance) reviewed(ctx context.Context, id data.ArchiveID, planID string) (archive.MaintenancePlan, archive.Plan, error) {
	p, err := o.state.LoadMaintenancePlan(ctx, planID)
	if err != nil || !p.Valid() || p.ArchiveID != id || p.Requester != o.jobs.Requester {
		return p, archive.Plan{}, result.New(result.PolicyRefused, nil)
	}
	original, err := o.state.LoadArchivePlanByArchive(ctx, id)
	if err != nil || !original.Valid() || original.Requester != o.jobs.Requester || original.ID != p.ArchivePlanID {
		return p, original, result.New(result.PolicyRefused, nil)
	}
	return p, original, nil
}
func (o publicArchiveMaintenance) ApplyMaintenance(ctx context.Context, a dispatch.ArchiveMaintenanceApplyArgs) (jobs.Accepted, error) {
	if !data.ValidID(string(a.ArchiveID)) || !jobs.ValidPlanID(a.PlanID) || !data.ValidID(a.IdempotencyKey) {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	_, p, err := o.reviewed(ctx, a.ArchiveID, a.PlanID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.ArchiveMaintenanceApply, App: p.Candidate.App, SecretRef: a.PlanID}, a.IdempotencyKey)
}
func (o publicArchiveMaintenance) resolveSource(ctx context.Context, id string) (ops.Operation, archive.MaintenancePlan, error) {
	original, err := o.state.GetOperation(ctx, id)
	if err != nil || original.Kind != ops.ArchiveMaintenanceApply || original.State != ops.RecoveryRequired || original.Requester != o.jobs.Requester {
		return original, archive.MaintenancePlan{}, result.New(result.PolicyRefused, nil)
	}
	p, err := o.state.LoadMaintenancePlan(ctx, original.SecretRef)
	if err != nil || !p.Valid() || p.Requester != original.Requester || p.Kind != archive.PurgeArchive && p.Kind != archive.ExpireArchive {
		return original, p, result.New(result.PolicyRefused, nil)
	}
	_, owner, err := o.reviewed(ctx, p.ArchiveID, p.ID)
	if err != nil || owner.Candidate.App != original.App {
		return original, p, result.New(result.PolicyRefused, nil)
	}
	deletion, err := o.state.ReadArchiveDeletion(ctx, p.ArchiveID)
	if err != nil || deletion.Sequence == 0 || deletion.PlanID != p.ID || deletion.Kind != p.Kind {
		return original, p, result.New(result.RecoveryRequired, nil)
	}
	return original, p, nil
}
func (o publicArchiveMaintenance) ResolveMaintenance(ctx context.Context, a dispatch.ArchiveMaintenanceResolveArgs) (jobs.Accepted, error) {
	if !jobs.ValidID(a.OperationID) || !data.ValidID(a.IdempotencyKey) {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	original, p, err := o.resolveSource(ctx, a.OperationID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.ArchiveMaintenanceResolve, App: original.App, SecretRef: p.ID, RecoveryOf: original.ID}, a.IdempotencyKey)
}
func (o publicArchiveMaintenance) task(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	if op.Kind != ops.ArchiveMaintenanceApply && op.Kind != ops.ArchiveMaintenanceResolve || op.Requester != o.jobs.Requester {
		return nil, result.New(result.PolicyRefused, nil)
	}
	p, err := o.state.LoadMaintenancePlan(ctx, op.SecretRef)
	if err != nil {
		return nil, result.New(result.PolicyRefused, nil)
	}
	_, owner, err := o.reviewed(ctx, p.ArchiveID, p.ID)
	if err != nil || owner.Candidate.App != op.App {
		return nil, result.New(result.PolicyRefused, nil)
	}
	var outcome archive.MaintenanceOutcome
	if op.Kind == ops.ArchiveMaintenanceResolve {
		original, resolved, err := o.resolveSource(ctx, op.RecoveryOf)
		if err != nil || original.SecretRef != op.SecretRef || resolved.ID != p.ID {
			return nil, result.New(result.PolicyRefused, nil)
		}
		deletion, err := o.maintenance.ReconcileDelete(ctx, p.ArchiveID)
		if err != nil {
			return nil, archiveMaintenanceFailure(err)
		}
		outcome = archive.MaintenanceOutcome{Kind: p.Kind, Deletion: &deletion}
	} else {
		var err error
		outcome, err = o.maintenance.Apply(ctx, p.ArchiveID, p.ID)
		if err != nil {
			return nil, archiveMaintenanceFailure(err)
		}
	}
	return archiveMaintenanceTaskReceipt(op, p, outcome)
}
func archiveMaintenanceTaskReceipt(op ops.Operation, p archive.MaintenancePlan, out archive.MaintenanceOutcome) (json.RawMessage, error) {
	receipt := archive.MaintenanceReceipt{Kind: string(op.Kind), OperationID: op.ID, OriginalOperationID: op.RecoveryOf, PlanID: p.ID, ArchiveID: p.ArchiveID, MaintenanceKind: p.Kind}
	if out.Kind != p.Kind {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	if out.Proof != nil && out.Deletion == nil && p.Kind == archive.VerifyRetention && out.Proof.ArchiveID == p.ArchiveID {
		receipt.Outcome = "verified"
		receipt.CompletedAt = out.Proof.VerifiedAt
		hash, err := archive.RetentionProofSHA256(*out.Proof)
		if err != nil {
			return nil, result.New(result.RecoveryRequired, nil)
		}
		receipt.EvidenceSHA256 = hash
	} else if out.Deletion != nil && out.Proof == nil && out.Deletion.Stage == archive.DeletionComplete && out.Deletion.ArchiveID == p.ArchiveID && out.Deletion.PlanID == p.ID && out.Deletion.Kind == p.Kind {
		receipt.Outcome = "local_deleted"
		receipt.CompletedAt = out.Deletion.CompletedAt
		raw, err := json.Marshal(*out.Deletion)
		if err != nil {
			return nil, result.New(result.RecoveryRequired, nil)
		}
		receipt.EvidenceSHA256 = "sha256:" + managedServiceHash(raw)
	} else {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	if !receipt.Valid() {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	return json.Marshal(receipt)
}
func (o publicArchiveMaintenance) PlanArchiveCredentials(ctx context.Context, a dispatch.ArchiveCredentialPlanArgs) (backupcredentials.ArchivePlan, error) {
	return o.credentials.Plan(ctx, a.ArchiveID, a.Binding, a.Epoch, a.ExpiresAt)
}
func (o publicArchiveMaintenance) SetArchiveCredentials(ctx context.Context, a dispatch.ArchiveCredentialSetArgs) (backupcredentials.ArchiveReceipt, error) {
	return o.credentials.DeliverScoped(ctx, a.ArchiveID, a.Binding, a.Epoch, a.PlanID, a.Packet)
}
func archiveMaintenanceFailure(err error) error {
	if errors.Is(err, archive.ErrRecovery) || errors.Is(err, backupcredentials.ErrStorage) {
		return result.New(result.RecoveryRequired, nil)
	}
	return result.New(result.PolicyRefused, nil)
}
func newPublicArchiveMaintenance(service Service, maintenance archive.MaintenanceService, credentials backupcredentials.ArchiveService, taskJobs jobs.Service) publicArchiveMaintenance {
	originalLock := credentials.Lock
	credentials.Lock = func(ctx context.Context) (func() error, error) {
		release, err := originalLock(ctx)
		if err != nil {
			return nil, err
		}
		if err = ensurePrivateChild(filepath.Dir(credentials.Files.Root), "credentials"); err != nil {
			_ = release()
			return nil, err
		}
		return release, nil
	}
	return publicArchiveMaintenance{state: service.Store, maintenance: maintenance, credentials: credentials, jobs: taskJobs}
}
