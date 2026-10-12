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

func (o nativeRetireOperations) task(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	p, err := o.state.LoadArchivePlan(ctx, op.SecretRef)
	if err != nil || !nativeArchiveCandidate(p.Candidate) || p.Candidate.App != op.App || p.Requester != op.Requester || op.Kind != ops.NativeRetire {
		return nil, result.New(result.PolicyRefused, nil)
	}
	workflow := o.workflow
	workflow.Requester = op.Requester
	m, err := workflow.Prepare(ctx, op.App, p.ID)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	completed, err := workflow.Retire(ctx, p.ArchiveID)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	manifestRaw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	completionRaw, err := json.Marshal(completed)
	if err != nil {
		return nil, err
	}
	return json.Marshal(archive.TaskReceipt{Format: "brine-native-archive-receipt-v1", OperationID: op.ID, PlanID: p.ID, ArchiveID: p.ArchiveID, ManifestSHA256: managedServiceHash(manifestRaw), CompletionSHA256: managedServiceHash(completionRaw), Outcome: "retired"})
}
