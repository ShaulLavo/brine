//go:build linux

package host

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeRetireOperations struct {
	workflow archive.Service
	jobs     jobs.Service
	state    *store.Store
}

func (o nativeRetireOperations) PlanNativeArchive(ctx context.Context, a dispatch.NativeRetirePlanArgs) (archive.Plan, error) {
	if !dispatch.ValidApp(a.App) {
		return archive.Plan{}, result.New(result.InvalidUsage, nil)
	}
	candidate, err := o.state.ArchiveCandidate(ctx, a.App)
	if err != nil || !nativeArchiveCandidate(candidate) {
		return archive.Plan{}, result.New(result.PolicyRefused, nil)
	}
	return o.workflow.Plan(ctx, a.App)
}
func (o nativeRetireOperations) RetireNativeArchive(ctx context.Context, a dispatch.NativeRetireApplyArgs) (jobs.Accepted, error) {
	if !dispatch.ValidApp(a.App) || !jobs.ValidPlanID(a.PlanID) || !jobs.ValidID(a.IdempotencyKey) {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	p, err := o.state.LoadArchivePlan(ctx, a.PlanID)
	if err != nil || !nativeArchiveCandidate(p.Candidate) || p.Candidate.App != a.App || p.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeRetire, App: a.App, SecretRef: p.ID}, a.IdempotencyKey)
}
func (o nativeRetireOperations) InspectNativeArchive(ctx context.Context, a dispatch.NativeRetireInspectArgs) (archive.Inspection, error) {
	return o.workflow.Inspect(ctx, a.ArchiveID)
}

func (o nativeRetireOperations) ResolveNativeArchive(ctx context.Context, a dispatch.NativeRetireResolveArgs) (jobs.Accepted, error) {
	if !jobs.ValidID(a.OperationID) || !jobs.ValidID(a.IdempotencyKey) {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	original, err := o.state.GetOperation(ctx, a.OperationID)
	if err != nil || original.Kind != ops.NativeRetire || original.State != ops.RecoveryRequired || original.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	p, err := o.state.LoadArchivePlan(ctx, original.SecretRef)
	if err != nil || !nativeArchiveCandidate(p.Candidate) || p.Candidate.App != original.App || p.Requester != original.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	if _, err = o.state.ReadArchiveManifest(ctx, p.ArchiveID); err != nil {
		return jobs.Accepted{}, result.New(result.RecoveryRequired, nil)
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeRetireResolve, App: original.App, SecretRef: p.ID, RecoveryOf: original.ID}, a.IdempotencyKey)
}

func (o nativeRetireOperations) resolveTask(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	original, err := o.state.GetOperation(ctx, op.RecoveryOf)
	if err != nil || op.Kind != ops.NativeRetireResolve || original.Kind != ops.NativeRetire || original.State != ops.RecoveryRequired || original.App != op.App || original.SecretRef != op.SecretRef || original.Requester != op.Requester {
		return nil, result.New(result.PolicyRefused, nil)
	}
	p, err := o.state.LoadArchivePlan(ctx, op.SecretRef)
	if err != nil || !nativeArchiveCandidate(p.Candidate) || p.Candidate.App != op.App || p.Requester != op.Requester {
		return nil, result.New(result.PolicyRefused, nil)
	}
	workflow := o.workflow
	workflow.Requester = op.Requester
	completed, err := workflow.Reconcile(ctx, p.ArchiveID)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	return o.retirementReceipt(ctx, op, p, completed)
}

func (o nativeRetireOperations) task(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	p, err := o.state.LoadArchivePlan(ctx, op.SecretRef)
	if err != nil || !nativeArchiveCandidate(p.Candidate) || p.Candidate.App != op.App || p.Requester != op.Requester || op.Kind != ops.NativeRetire {
		return nil, result.New(result.PolicyRefused, nil)
	}
	workflow := o.workflow
	workflow.Requester = op.Requester
	_, err = workflow.Prepare(ctx, op.App, p.ID)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	completed, err := workflow.Retire(ctx, p.ArchiveID)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	return o.retirementReceipt(ctx, op, p, completed)
}

func (o nativeRetireOperations) retirementReceipt(ctx context.Context, op ops.Operation, p archive.Plan, completed archive.Disposition) (json.RawMessage, error) {
	m, err := o.state.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil {
		return nil, err
	}
	manifestRaw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	completionRaw, err := json.Marshal(completed)
	if err != nil {
		return nil, err
	}
	return json.Marshal(archive.TaskReceipt{Format: "brine-native-archive-receipt-v1", OperationID: op.ID, OriginalOperationID: op.RecoveryOf, PlanID: p.ID, ArchiveID: p.ArchiveID, ManifestSHA256: managedServiceHash(manifestRaw), CompletionSHA256: managedServiceHash(completionRaw), Outcome: "retired"})
}
