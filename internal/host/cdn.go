package host

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
)

type cdnOperations struct{ service Service }

var _ cdnoperator.Host = cdnOperations{}

func (o cdnOperations) ready() error {
	if o.service.Requester == "" {
		return result.New(result.DispatchOperationRefused, nil)
	}
	if o.service.Store == nil || o.service.Policy == nil || o.service.Inventory == nil {
		return result.New(result.DependencyMissing, nil)
	}
	return nil
}

func (o cdnOperations) Context(ctx context.Context, request cdnoperator.PlanRequest) (cdnoperator.Planning, error) {
	if o.ready() != nil || !request.Valid() {
		return cdnoperator.Planning{}, result.New(result.DispatchOperationRefused, nil)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return cdnoperator.Planning{}, cdnHostError(err)
	}
	defer func() { _ = lock.Release() }()
	return o.planning(ctx, request)
}

func (o cdnOperations) planning(ctx context.Context, request cdnoperator.PlanRequest) (cdnoperator.Planning, error) {
	pol, err := o.service.Policy.Load(ctx)
	if err != nil {
		return cdnoperator.Planning{}, cdnHostError(err)
	}
	binding, ok := pol.CDNBinding(request.Binding, request.App)
	if !ok {
		return cdnoperator.Planning{}, cdn.ErrConflict
	}
	snapshot, err := o.service.Inventory.Collect(ctx)
	if err != nil {
		return cdnoperator.Planning{}, cdnHostError(err)
	}
	identity, _ := json.Marshal(snapshot.Identity)
	if binding.Target != cdn.Hash(identity) {
		return cdnoperator.Planning{}, cdn.ErrConflict
	}
	release, err := o.service.Store.CurrentRelease(ctx, request.App)
	if err != nil {
		return cdnoperator.Planning{}, cdnHostError(err)
	}
	_, desired, err := o.service.Store.LoadPlan(ctx, release.PlanID)
	if err != nil || !slices.Contains(desired.Domains, spec.Domain(binding.Hostname)) {
		return cdnoperator.Planning{}, cdn.ErrConflict
	}
	profile, ok := bindingProfile(binding, request.Profile)
	if !ok {
		return cdnoperator.Planning{}, cdn.ErrConflict
	}
	a := cdn.Attachment{Mode: request.Mode, Owner: cdn.AttachmentFor(binding.Target, request.App, release.ID), Binding: binding, Profile: profile, SourceRelease: release.ID}
	planning := cdnoperator.Planning{Context: cdn.PlanningContext{Attachment: a, PolicyHash: cdn.Fingerprint(pol.Hash()), RecoveryOf: request.RecoverOperation}}
	planning, err = o.planningHistory(ctx, request, planning)
	if err != nil {
		return planning, cdnHostError(err)
	}
	if planning.Context.Attachment.Revision == "" {
		planning.Context.Attachment.Revision = cdn.RevisionFor(planning.Context.Attachment, planning.Context.PolicyHash, planning.Context.Previous)
	}
	planning.Sample, err = cdn.NewAssetSample(binding.Cache, request.SamplePath)
	return planning, cdnHostError(err)
}

func bindingProfile(binding cdn.Binding, reference cdn.ProfileRef) (cdn.ProfileScope, bool) {
	for _, profile := range binding.AllowedEdges {
		if profile.Reference == reference {
			return profile, true
		}
	}
	return cdn.ProfileScope{}, false
}

func (o cdnOperations) planningHistory(ctx context.Context, request cdnoperator.PlanRequest, planning cdnoperator.Planning) (cdnoperator.Planning, error) {
	head, err := o.service.Store.CDNHead(ctx, request.App)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return planning, cdnHostError(err)
	}
	if err == nil {
		if head.Attachment.Binding.Hostname != planning.Context.Attachment.Binding.Hostname || head.Attachment.Owner.Target != planning.Context.Attachment.Binding.Target {
			return planning, cdn.ErrConflict
		}
		planning.Context.Attachment.Owner = head.Attachment.Owner
		planning.Context.Previous, planning.Context.Retained = head.Attachment.Revision, head.Receipts
		previous := head.Attachment
		previous.Revision = ""
		if reflect.DeepEqual(planning.Context.Attachment, previous) {
			planning.Context.Attachment.Revision = head.Attachment.Revision
		}
	}
	if request.Revision != "" {
		retained, err := o.service.Store.CDNRevision(ctx, request.App, request.Revision)
		if err != nil || retained.Attachment.Owner != planning.Context.Attachment.Owner || retained.Attachment.Profile != planning.Context.Attachment.Profile || retained.Attachment.Mode != request.Mode || !reflect.DeepEqual(retained.Attachment.Binding, planning.Context.Attachment.Binding) {
			return planning, cdn.ErrConflict
		}
	}
	if request.RecoverOperation != "" {
		source, err := o.ownedStatus(ctx, request.RecoverOperation)
		if err != nil || source.Plan.Context.Attachment.Owner != planning.Context.Attachment.Owner || source.Plan.Context.Attachment.Owner.App != request.App {
			return planning, cdn.ErrConflict
		}
		planning.Context.Retained, err = o.service.Store.CDNReceipts(ctx, string(request.RecoverOperation))
		if err != nil {
			return planning, err
		}
	}
	return planning, nil
}

func (o cdnOperations) validatePlan(ctx context.Context, p cdn.Plan) error {
	if p.Validate() != nil {
		return cdn.ErrInvalid
	}
	a := p.Context.Attachment
	request := cdnoperator.PlanRequest{App: a.Owner.App, Binding: a.Binding.Reference, Profile: a.Profile.Reference, Mode: a.Mode, SamplePath: p.Sample.Path, RecoverOperation: p.Context.RecoveryOf}
	current, err := o.planning(ctx, request)
	if err != nil {
		return err
	}
	context := p.Context
	context.Initial = nil
	want, err := cdn.NewPlan(current.Context, current.Sample, nil, nil)
	if err != nil {
		return err
	}
	normalized, err := cdn.NewPlan(context, p.Sample, nil, nil)
	if err != nil || normalized.ID != want.ID {
		return cdn.ErrConflict
	}
	return nil
}

func (o cdnOperations) SavePlan(ctx context.Context, p cdn.Plan) (cdn.Plan, error) {
	if err := o.ready(); err != nil {
		return cdn.Plan{}, cdnHostError(err)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return cdn.Plan{}, cdnHostError(err)
	}
	defer func() { _ = lock.Release() }()
	if err := o.validatePlan(ctx, p); err != nil {
		return cdn.Plan{}, cdnHostError(err)
	}
	id, err := o.service.Store.SaveCDNPlan(ctx, p)
	if err != nil {
		return cdn.Plan{}, cdnHostError(err)
	}
	saved, err := o.service.Store.LoadCDNPlan(ctx, id)
	return saved, cdnHostError(err)
}

func (o cdnOperations) Apply(ctx context.Context, request cdnoperator.ApplyRequest) (cdnoperator.Progress, error) {
	if err := o.ready(); err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	defer func() { _ = lock.Release() }()
	p, err := o.service.Store.LoadCDNPlan(ctx, request.PlanID)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	// Replayed admission remains inspectable even after policy or app changes.
	op, err := o.service.Store.CDNByKey(ctx, o.service.Requester, request.IdempotencyKey)
	if errors.Is(err, store.ErrNotFound) {
		if err = o.validatePlan(ctx, p); err != nil {
			return cdnoperator.Progress{}, cdnHostError(err)
		}
	} else if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	op, _, err = o.service.Store.AdmitCDN(ctx, request.PlanID, o.service.Requester, request.IdempotencyKey)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	return o.progress(ctx, cdn.OperationID(op.ID), nil)
}

func (o cdnOperations) ownedStatus(ctx context.Context, operation cdn.OperationID) (cdn.Status, error) {
	if err := o.ready(); err != nil {
		return cdn.Status{}, cdnHostError(err)
	}
	op, err := o.service.Store.GetOperation(ctx, string(operation))
	if err != nil {
		return cdn.Status{}, cdnHostError(err)
	}
	if op.Kind != ops.CDN || op.Requester != o.service.Requester {
		return cdn.Status{}, result.New(result.DispatchOperationRefused, nil)
	}
	status, err := o.service.Store.CDNStatus(ctx, string(operation))
	return status, cdnHostError(err)
}

func (o cdnOperations) progress(ctx context.Context, operation cdn.OperationID, work cdn.Work) (cdnoperator.Progress, error) {
	status, err := o.ownedStatus(ctx, operation)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	if work == nil {
		work, err = o.service.Store.NextCDN(ctx, string(operation))
	}
	return cdnoperator.Progress{Operation: operation, Plan: status.Plan, Work: work}, cdnHostError(err)
}

func (o cdnOperations) Next(ctx context.Context, request cdnoperator.OperationRequest) (cdnoperator.Progress, error) {
	return o.progress(ctx, request.Operation, nil)
}

func (o cdnOperations) Seal(ctx context.Context, request cdnoperator.SealRequest) (cdnoperator.Progress, error) {
	if err := o.ready(); err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	defer func() { _ = lock.Release() }()
	status, err := o.ownedStatus(ctx, request.Operation)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	if int(request.StepIndex) >= len(status.Effects) {
		if err := o.validatePlan(ctx, status.Plan); err != nil {
			return cdnoperator.Progress{}, cdnHostError(err)
		}
	}
	work, err := o.service.Store.ClaimCDN(ctx, string(request.Operation), request.StepIndex, request.Sealed)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	return o.progress(ctx, request.Operation, work)
}

func (o cdnOperations) RecordResult(ctx context.Context, request cdnoperator.ResultRequest) (cdnoperator.Progress, error) {
	if _, err := o.ownedStatus(ctx, request.Operation); err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	work, err := o.service.Store.RecordCDNResult(ctx, string(request.Operation), request.Result)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	return o.progress(ctx, request.Operation, work)
}

func (o cdnOperations) RecordInspection(ctx context.Context, request cdnoperator.InspectionRequest) (cdnoperator.Progress, error) {
	if _, err := o.ownedStatus(ctx, request.Operation); err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	work, err := o.service.Store.RecordCDNInspection(ctx, string(request.Operation), request.Inspection)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	return o.progress(ctx, request.Operation, work)
}

func (o cdnOperations) Qualify(ctx context.Context, request cdnoperator.ProofRequest) (cdnoperator.Progress, error) {
	if err := o.ready(); err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	defer func() { _ = lock.Release() }()
	status, err := o.ownedStatus(ctx, request.Operation)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	if err := o.validatePlan(ctx, status.Plan); err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	work, err := o.service.Store.CommitCDN(ctx, string(request.Operation), request.Proof, request.Readback)
	if err != nil {
		return cdnoperator.Progress{}, cdnHostError(err)
	}
	return o.progress(ctx, request.Operation, work)
}

func (o cdnOperations) Status(ctx context.Context, request cdnoperator.StatusRequest) (cdnoperator.Status, error) {
	status := cdnoperator.Status{App: request.App}
	if err := o.ready(); err != nil {
		return status, cdnHostError(err)
	}
	head, err := o.service.Store.CDNHead(ctx, request.App)
	if err == nil {
		status.Head = &head
		p, err := o.service.Store.LoadCDNPlan(ctx, head.Plan)
		if err != nil {
			return status, cdnHostError(err)
		}
		status.Plan = &p
	} else if !errors.Is(err, store.ErrNotFound) {
		return status, cdnHostError(err)
	}
	operation := request.Operation
	if operation == "" {
		operation, err = o.pending(ctx, request.App)
		if err != nil {
			return status, cdnHostError(err)
		}
	}
	if operation == "" {
		return status, nil
	}
	pending, err := o.ownedStatus(ctx, operation)
	if err != nil {
		return status, cdnHostError(err)
	}
	if pending.Plan.Context.Attachment.Owner.App != request.App {
		return status, cdn.ErrConflict
	}
	status.Pending, status.Plan = &pending, &pending.Plan
	return status, nil
}

func (o cdnOperations) pending(ctx context.Context, app string) (cdn.OperationID, error) {
	operations, err := o.service.Store.ListUnfinished(ctx)
	if err != nil {
		return "", cdnHostError(err)
	}
	for _, op := range operations {
		if op.Kind == ops.CDN && op.App == app && op.Requester == o.service.Requester {
			return cdn.OperationID(op.ID), nil
		}
	}
	return "", nil
}

func cdnHostError(err error) error {
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		return result.New(result.Conflict, err)
	}
	if errors.Is(err, store.ErrInvalid) {
		return result.New(result.InvalidUsage, err)
	}
	return err
}
