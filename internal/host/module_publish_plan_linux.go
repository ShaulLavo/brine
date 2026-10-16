//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

type modulePublications struct {
	service Service
	runtime nativeBackupRuntime
	jobs    jobs.Service
}

func (o modulePublications) Plan(ctx context.Context, request dispatch.ModulePlanArgs) (datapublish.ReviewView, error) {
	if !request.Valid() {
		return datapublish.ReviewView{}, datapublish.ErrRecord
	}
	planned, err := o.plan(ctx, request)
	if errors.Is(err, datapublish.ErrOperatorProcedure) {
		err = result.New(result.NativeModuleSchemaRefused, err)
	}
	return planned.ReviewView(), err
}

func (o modulePublications) plan(ctx context.Context, request dispatch.ModulePlanArgs) (_ datapublish.Plan, err error) {
	if o.service.Authorize(ctx, dispatch.Mutating) != nil || o.runtime.state != o.service.Store || o.runtime.routes == nil {
		return datapublish.Plan{}, datapublish.ErrRecord
	}
	artifact, err := datapublish.NewArtifact(request.Wasm)
	if err != nil {
		return datapublish.Plan{}, err
	}
	requester, err := datainit.ParseRequester(o.service.Requester)
	if err != nil {
		return datapublish.Plan{}, err
	}
	lock, err := o.service.Store.AcquireHostLock(ctx)
	if err != nil {
		return datapublish.Plan{}, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	routes, current, prior, err := o.runtime.preparedRouteStates(ctx, o.service, request.PreparedPointID, request.App, request.TargetDeploymentPlanID)
	if err != nil {
		return datapublish.Plan{}, err
	}
	opened, err := o.runtime.reopenPreparedSource(ctx, request.PreparedPointID, routes, current, prior)
	if err != nil {
		return datapublish.Plan{}, err
	}
	var descriptor *descriptorLease
	var stage *publicationStageLease
	defer func() {
		err = errors.Join(err, finishPublicationReview(opened, descriptor, stage))
	}()
	preparation, err := o.service.Store.PreparePublicationReview(ctx, opened.preparation, requester, store.PublicationReviewRequest{App: request.App, PreparedPointID: request.PreparedPointID, TargetDeploymentPlanID: request.TargetDeploymentPlanID, Artifact: artifact, RecoveryReleaseIDs: request.RecoveryReleaseIDs})
	if err != nil {
		return datapublish.Plan{}, err
	}
	descriptor, err = acquireDescriptorLease(ctx, o.service.Store, opened.preparation, opened.restored.permit.Runtime.BindingID, request.TargetDeploymentPlanID, artifact, podman.New(o.runtime.session))
	if err != nil {
		return datapublish.Plan{}, err
	}
	extracted, err := extractPublicationCandidate(ctx, descriptor, localexec.ExecRunner{}, o.runtime.session)
	if err != nil {
		return datapublish.Plan{}, err
	}
	work, err := beginPublicationReviewWork(ctx, opened.restored, preparation, extracted)
	if err != nil {
		return datapublish.Plan{}, err
	}
	if opened.restored.source.Kind() == "unpublished" {
		return completeInitialPublicationReview(ctx, work)
	}
	stage, err = acquirePublicationReviewStage(ctx, work, podman.New(o.runtime.session))
	if err != nil {
		return datapublish.Plan{}, err
	}
	preflight, err := runPublicationReviewPreflight(ctx, stage, localexec.ExecRunner{}, o.runtime.session)
	if err != nil {
		return datapublish.Plan{}, err
	}
	return preflight.complete(ctx)
}

func finishPublicationReview(opened *nativePreparedReopen, descriptor *descriptorLease, stage *publicationStageLease) error {
	if !publicationReviewProcessesKnown(descriptor, stage) {
		return nil
	}
	if stage != nil {
		stage.material.clear()
	}
	var err error
	if descriptor != nil && descriptor.artifact != nil {
		err = descriptor.artifact.discard()
	}
	return errors.Join(err, opened.close())
}

func publicationReviewProcessesKnown(descriptor *descriptorLease, stage *publicationStageLease) bool {
	if descriptor != nil && descriptor.attempted && (!descriptor.disposed || descriptor.process != nil || descriptor.containerID != "") {
		return false
	}
	return stage == nil || !stage.attempted || stage.disposed && stage.process == nil && stage.containerID == ""
}
