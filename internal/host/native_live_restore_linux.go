//go:build linux

package host

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeLiveRestoreOperations struct {
	service Service
	jobs    jobs.Service
	runtime nativeBackupRuntime
	routes  *caddy.Manager
}

func (o nativeLiveRestoreOperations) Plan(ctx context.Context, request liverestore.PlanRequest) (liverestore.Plan, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return liverestore.Plan{}, err
	}
	if _, err = liverestore.DecodePlanRequest(raw); err != nil {
		return liverestore.Plan{}, result.New(result.InvalidUsage, nil)
	}
	requester, err := datainit.ParseRequester(o.jobs.Requester)
	if err != nil {
		return liverestore.Plan{}, err
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || !pol.AllowsLiveRestore() {
		return liverestore.Plan{}, result.New(result.PolicyRefused, nil)
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return liverestore.Plan{}, err
	}
	defer func() { _ = lock.Release() }()
	point, err := o.service.Store.ReadNativePreparedPoint(ctx, request.CurrentPreparedPointID)
	if err != nil {
		return liverestore.Plan{}, err
	}
	preparation, err := o.service.Store.AcquireEnginePreparation(ctx, point.BindingID, point.FenceID, point.FenceOwnerOpID)
	if err != nil {
		return liverestore.Plan{}, err
	}
	defer func() { _ = preparation.Close() }()
	return o.service.Store.BuildNativeLiveRestorePlan(ctx, preparation, requester, request, pol)
}

func (o nativeLiveRestoreOperations) Apply(ctx context.Context, request liverestore.ApplyRequest) (jobs.Accepted, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return jobs.Accepted{}, err
	}
	if _, err = liverestore.DecodeApplyRequest(raw); err != nil {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	p, err := o.service.Store.LoadNativeLiveRestorePlan(ctx, request.PlanID)
	if err != nil || p.Spec().Facts.App != request.App || p.Spec().Requester.String() != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || !pol.AllowsLiveRestore() || pol.Hash() != "sha256:"+p.Spec().Facts.PolicySHA256 || !time.Now().UTC().Before(p.Spec().ExpiresAt) {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeLiveRestore, App: request.App, SecretRef: request.PlanID}, request.IdempotencyKey)
}

func (o nativeLiveRestoreOperations) Inspect(ctx context.Context, request liverestore.InspectRequest) (liverestore.Progress, error) {
	event, err := o.service.Store.ReadNativeLiveRestoreProgress(ctx, request.OperationID)
	if err != nil {
		return liverestore.Progress{}, err
	}
	op, err := o.service.Store.GetOperation(ctx, request.OperationID)
	if err != nil || op.Requester != o.jobs.Requester {
		return liverestore.Progress{}, result.New(result.PolicyRefused, nil)
	}
	return liverestore.ProgressFromEvent(event), nil
}

func (o nativeLiveRestoreOperations) task(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	p, err := o.service.Store.LoadNativeLiveRestorePlan(ctx, op.SecretRef)
	if err != nil || op.Kind != ops.NativeLiveRestore || p.Spec().Requester.String() != op.Requester || p.Spec().Facts.App != op.App {
		return nil, result.New(result.PolicyRefused, nil)
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || !pol.AllowsLiveRestore() || pol.Hash() != "sha256:"+p.Spec().Facts.PolicySHA256 {
		return nil, result.New(result.PolicyRefused, nil)
	}
	hostLock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = hostLock.Release() }()
	spec := p.Spec()
	preparation, err := o.service.Store.AcquireEnginePreparation(ctx, spec.Facts.Head.BindingID, spec.Safety.FenceID, spec.Safety.FenceOwnerOpID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = preparation.Close() }()
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, o.runtime.stateRoot, spec.Facts.Head.BindingID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lifetime.Release() }()
	stopped, err := o.runtime.reopenNativeRestoreStop(ctx, p, preparation, lifetime, o.service)
	if err != nil {
		return nil, err
	}
	permit, err := o.service.Store.ReadNativeStoppedPermit(ctx, stopped)
	if err != nil {
		return nil, err
	}
	// A completed exact point is independently copied and sealed again before
	// extraction. The original source is never deleted by copy cleanup.
	staging := filepath.Join(o.runtime.stateRoot, "native-restore-sources")
	if err = ensurePrivateChild(o.runtime.stateRoot, "native-restore-sources"); err != nil {
		return nil, err
	}
	nonce, err := data.NewID()
	if err != nil {
		return nil, err
	}
	copied, err := copyEngineSource(ctx, permit.Reserved.Database, staging, nonce, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		return nil, err
	}
	defer func() { _ = copied.discard() }()
	source, err := reopenNativeCheckpoint(ctx, o.service.Store, stopped, copied)
	if err != nil {
		return nil, err
	}
	if _, err = o.service.Store.BeginNativeLiveRestore(ctx, stopped, p.ID(), op.ID, pol); err != nil {
		return nil, err
	}
	claim, err := o.service.Store.RecordNativeLiveRestoreDownloadIntent(ctx, stopped, op.ID, pol)
	if err != nil {
		return nil, err
	}
	stage, err := newNativeRestoreStage(ctx, o.service.Store, claim, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stage.close() }()
	event, err := o.runtime.executeNativeLiveRestore(ctx, stage, preparation, lifetime, o.routes)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	a := event.Attempt()
	return json.Marshal(liverestore.Receipt{Format: "brine-native-live-restore-receipt-v1", OperationID: op.ID, PlanID: p.ID(), BindingID: a.BindingID, SelectedPointID: a.SelectedPointID, EpochID: a.IncomingEpochID, CompletionProofSHA256: event.ProofSHA256(), Outcome: "restored_stopped"})
}

func (r nativeBackupRuntime) reopenNativeRestoreStop(ctx context.Context, p liverestore.Plan, preparation *store.EnginePreparation, lifetime *data.EngineLock, service Service) (*store.NativeStoppedPreparation, error) {
	spec := p.Spec()
	original, err := r.state.ReadNativePreparedStop(ctx, spec.Safety.PointID)
	if err != nil || original.Mode != "clean_exit" {
		return nil, errEngineSource
	}
	permit, err := r.state.ReadEnginePermit(ctx, spec.Facts.Head.BindingID)
	if err != nil || verifyEngineService(ctx, r.home, permit) != nil {
		return nil, errEngineSource
	}
	host := archiveHost{session: r.session}
	stopped, err := host.serviceStopped(ctx, engineContainerName(permit.Runtime.BindingID)+".service", false)
	if err != nil || !stopped {
		return nil, errEngineSource
	}
	containers, err := listEngineContainers(ctx, r.session)
	if err != nil || !engineBindingAbsent(containers, permit) {
		return nil, errEngineSource
	}
	inspected, err := r.session.Execute(ctx, "/usr/bin/podman", []string{"container", "inspect", original.ContainerID}, nil, false)
	if err != nil {
		return nil, errEngineSource
	}
	finished, err := decodeNativeStoppedState([]byte(inspected.Stdout), original.ContainerID, time.Time{}, time.Now().UTC())
	if err != nil || !finished.Equal(original.FinishedAt) {
		return nil, errEngineSource
	}
	if err := r.verifyRestoreIngress(ctx, service, spec.Safety.PointID, spec.Facts.App, permit.Runtime.BindingID); err != nil {
		return nil, err
	}
	// The retained original completion is compared by Store to this fresh stop.
	current := store.NativeStopObservation{Mode: "clean_exit", ContainerID: original.ContainerID, FinishedAt: finished, SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true}
	return r.state.ResumeNativePreparedPoint(ctx, preparation, lifetime, spec.Safety.PointID, current)
}

func (o nativeLiveRestoreOperations) ReviewResolution(ctx context.Context, request liverestore.InspectRequest) (liverestore.Resolution, error) {
	requester, err := datainit.ParseRequester(o.jobs.Requester)
	if err != nil {
		return liverestore.Resolution{}, err
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || !pol.AllowsLiveRestore() {
		return liverestore.Resolution{}, result.New(result.PolicyRefused, nil)
	}
	op, err := o.service.Store.GetOperation(ctx, request.OperationID)
	if err != nil || op.Kind != ops.NativeLiveRestore || op.State != ops.RecoveryRequired || op.Requester != requester.String() {
		return liverestore.Resolution{}, result.New(result.PolicyRefused, nil)
	}
	p, err := o.service.Store.LoadNativeLiveRestorePlan(ctx, op.SecretRef)
	if err != nil {
		return liverestore.Resolution{}, err
	}
	hostLock, preparation, lifetime, err := o.restoreResolutionLocks(ctx, p)
	if err != nil {
		return liverestore.Resolution{}, err
	}
	defer func() { _ = lifetime.Release(); _ = preparation.Close(); _ = hostLock.Release() }()
	view, err := o.service.Store.ReadNativeRestoreResolutionTarget(ctx, preparation, lifetime, op.ID)
	if err != nil {
		return liverestore.Resolution{}, err
	}
	reserved, err := o.service.Store.ReadReservedEngine(ctx, view.Runtime.BindingID)
	if err != nil {
		return liverestore.Resolution{}, err
	}
	tree := &engineTransitionTree{preparation: preparation, lifetime: lifetime, binding: reserved.Database, input: view.Input, limits: engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16}}
	observed, err := o.runtime.observeNativeRestoreTransition(ctx, view, tree, o.routes)
	if err != nil {
		return liverestore.Resolution{}, err
	}
	return o.service.Store.ReviewNativeRestoreResolution(ctx, preparation, lifetime, op.ID, requester, pol, observed)
}

func (o nativeLiveRestoreOperations) Resolve(ctx context.Context, request liverestore.ResolveRequest) (jobs.Accepted, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return jobs.Accepted{}, err
	}
	if _, err = liverestore.DecodeResolveRequest(raw); err != nil {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	review, err := o.service.Store.ReadNativeRestoreResolution(ctx, request.ReviewedResolutionPlanID)
	if err != nil || review.OriginalOperationID != request.OriginalOperationID || review.Requester.String() != o.jobs.Requester || !time.Now().UTC().Before(review.ExpiresAt) {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	original, err := o.service.Store.GetOperation(ctx, request.OriginalOperationID)
	if err != nil || original.State != ops.RecoveryRequired || original.SecretRef != review.ApplyPlanID || original.Requester != o.jobs.Requester {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	p, err := o.service.Store.LoadNativeLiveRestorePlan(ctx, review.ApplyPlanID)
	if err != nil {
		return jobs.Accepted{}, err
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || !pol.AllowsLiveRestore() || pol.Hash() != "sha256:"+p.Spec().Facts.PolicySHA256 {
		return jobs.Accepted{}, result.New(result.PolicyRefused, nil)
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.NativeLiveRestoreResolve, App: original.App, SecretRef: review.ID(), RecoveryOf: original.ID}, request.IdempotencyKey)
}

func (o nativeLiveRestoreOperations) restoreResolutionLocks(ctx context.Context, p liverestore.Plan) (store.Lock, *store.EnginePreparation, *data.EngineLock, error) {
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	spec := p.Spec()
	preparation, err := o.service.Store.AcquireEnginePreparation(ctx, spec.Facts.Head.BindingID, spec.Safety.FenceID, spec.Safety.FenceOwnerOpID)
	if err != nil {
		_ = lock.Release()
		return nil, nil, nil, err
	}
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, o.runtime.stateRoot, spec.Facts.Head.BindingID)
	if err != nil {
		_ = preparation.Close()
		_ = lock.Release()
		return nil, nil, nil, err
	}
	return lock, preparation, lifetime, nil
}

func (o nativeLiveRestoreOperations) resolveTask(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
	review, err := o.service.Store.ReadNativeRestoreResolution(ctx, op.SecretRef)
	if err != nil || op.Kind != ops.NativeLiveRestoreResolve || review.OriginalOperationID != op.RecoveryOf || review.Requester.String() != op.Requester {
		return nil, result.New(result.PolicyRefused, nil)
	}
	p, err := o.service.Store.LoadNativeLiveRestorePlan(ctx, review.ApplyPlanID)
	if err != nil {
		return nil, err
	}
	pol, err := o.service.Policy.Load(ctx)
	if err != nil || !pol.AllowsLiveRestore() || pol.Hash() != "sha256:"+p.Spec().Facts.PolicySHA256 {
		return nil, result.New(result.PolicyRefused, nil)
	}
	lock, preparation, lifetime, err := o.restoreResolutionLocks(ctx, p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lifetime.Release(); _ = preparation.Close(); _ = lock.Release() }()
	view, err := o.service.Store.ReadNativeRestoreResolutionTarget(ctx, preparation, lifetime, op.RecoveryOf)
	if err != nil {
		return nil, err
	}
	reserved, err := o.service.Store.ReadReservedEngine(ctx, view.Runtime.BindingID)
	if err != nil {
		return nil, err
	}
	tree := &engineTransitionTree{preparation: preparation, lifetime: lifetime, binding: reserved.Database, input: view.Input, limits: engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16}}
	observed, err := o.runtime.observeNativeRestoreTransition(ctx, view, tree, o.routes)
	if err != nil {
		return nil, err
	}
	capability, err := o.service.Store.ResumeNativeLiveRestoreTransition(ctx, preparation, lifetime, op.RecoveryOf, op.ID, observed)
	if err != nil {
		return nil, err
	}
	if view.Cursor.Sequence == 0 {
		incoming, err := o.service.Store.ResumeNativeRestoreIncomingRepository(ctx, capability)
		if err != nil {
			return nil, err
		}
		metadata, err := incoming.Metadata(ctx)
		if err != nil {
			return nil, err
		}
		if metadata.InitializationStage != "initialized" {
			workspace := filepath.Join(string(reserved.Database.Root), "transitions", view.Input.Common().Scope.ID)
			if _, err = o.runtime.initializeNativeRestoreRepository(ctx, incoming, workspace); err != nil {
				return nil, err
			}
		}
		if err = o.service.Store.PrepareResolvedNativeRestoreTransition(ctx, capability, incoming, observed); err != nil {
			return nil, err
		}
	}
	event, err := o.runtime.finishNativeLiveRestore(ctx, capability, preparation, lifetime, reserved.Database, o.routes)
	if err != nil {
		return nil, result.New(result.RecoveryRequired, nil)
	}
	a := event.Attempt()
	return json.Marshal(liverestore.Receipt{Format: "brine-native-live-restore-receipt-v1", OperationID: op.ID, PlanID: review.ID(), OriginalOperationID: op.RecoveryOf, BindingID: a.BindingID, SelectedPointID: a.SelectedPointID, EpochID: a.IncomingEpochID, CompletionProofSHA256: event.ProofSHA256(), Outcome: "restored_stopped"})
}

func (r nativeBackupRuntime) verifyRestoreIngress(ctx context.Context, service Service, pointID, app string, binding data.ReplicaBindingID) error {
	point, err := r.state.ReadNativePreparedPoint(ctx, pointID)
	if err != nil || point.BindingID != binding {
		return errEngineSource
	}
	record, err := r.state.ReadNativePreparedRoutes(ctx, pointID)
	if err != nil || record.App != app {
		return errEngineSource
	}
	_, current, _, err := r.preparedObservedRouteStates(ctx, service, record)
	if err != nil {
		return err
	}
	if _, present := current.Files[app+".caddy"]; present {
		return errEngineSource
	}
	return ctx.Err()
}
