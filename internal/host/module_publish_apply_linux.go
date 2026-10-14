//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationApplyWork struct {
	operations  modulePublications
	op          ops.Operation
	opened      *nativePreparedReopen
	artifact    *publicationArtifact
	attempt     *store.PublicationAttempt
	working     *publicationWorkingStage
	preflight   *publicationStageLease
	publication *publicationStageLease
	target      *publicationTargetStage
	routes      *caddy.Manager
	withdrawn   caddy.State
	previous    caddy.State
	stop        store.NativeStopObservation
}

func modulePublicationApplyTask(operations modulePublications) func(context.Context, ops.Operation) (json.RawMessage, error) {
	return func(ctx context.Context, op ops.Operation) (_ json.RawMessage, err error) {
		if op.Kind != ops.ModulePublishApply {
			return nil, datapublish.ErrRecord
		}
		scoped, err := operations.forTask(ctx, op)
		if err != nil {
			return nil, err
		}
		lock, err := scoped.service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, lock.Release()) }()
		work := &publicationApplyWork{operations: scoped, op: op}
		defer func() { err = errors.Join(err, work.close()) }()
		if err = work.open(ctx); err != nil {
			return nil, err
		}
		checkpoint, err := work.publish(ctx)
		if err != nil {
			return nil, err
		}
		receipt, err := work.transition(ctx, checkpoint)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
}

func (w *publicationApplyWork) open(ctx context.Context) error {
	s := w.operations.service.Store
	review, err := s.ReadPublicationReview(ctx, w.op.SecretRef)
	if err != nil {
		return err
	}
	inspection, err := s.PublicationInspection(ctx, w.op.SecretRef, w.op.Requester)
	if err != nil || inspection.OperationID != "" {
		return datapublish.ErrInspection
	}
	_, w.stop, _, err = s.ReadNativePreparedStopExpectation(ctx, review.Plan().Source().PointID())
	if err != nil {
		return err
	}
	declaration, err := s.PublicationDeclarationID(ctx, w.op.SecretRef, w.op.Requester)
	if err != nil {
		return err
	}
	w.routes, w.withdrawn, w.previous, err = w.operations.runtime.preparedRouteStates(ctx, w.operations.service, review.Plan().Source().PointID(), w.op.App, declaration)
	if err != nil {
		return err
	}
	w.opened, err = w.operations.runtime.reopenPreparedSource(ctx, review.Plan().Source().PointID(), w.routes, w.withdrawn, w.previous)
	if err != nil {
		return err
	}
	w.attempt, err = s.BeginPublicationAttempt(ctx, w.opened.preparation, w.op.SecretRef, w.op.ID, time.Now().UTC())
	if err != nil {
		return err
	}
	if _, err = s.RecordPublicationIntent(ctx, w.attempt, datapublish.RestoreStage); err != nil {
		return err
	}
	w.artifact, err = stagePublicationArtifact(ctx, w.opened.preparation, w.opened.restored.permit.Runtime.BindingID, review.Artifact())
	if err != nil {
		return err
	}
	w.working, err = beginPublicationWorkingStage(ctx, w.opened.restored, w.attempt, w.artifact)
	return err
}

func (w *publicationApplyWork) publish(ctx context.Context) (publicationTargetCheckpoint, error) {
	zero := publicationTargetCheckpoint{}
	s := w.operations.service.Store
	working := w.working
	if working == nil || working.unchanged(ctx) != nil {
		return zero, datapublish.ErrRecord
	}
	var err error
	if _, err = s.RecordPublicationIntent(ctx, w.attempt, datapublish.CheckPreflight); err != nil {
		return zero, err
	}
	if w.opened.restored.source.Kind() == "unpublished" {
		err = recordInitialPublicationPreflight(ctx, working)
	} else {
		w.preflight, err = acquireRestoredPublicationStage(ctx, working, podman.New(w.operations.runtime.session))
		if err == nil {
			_, err = runRestoredPublicationStage(ctx, w.preflight, s, w.attempt, moduleStagePreflight, localexec.ExecRunner{}, w.operations.runtime.session)
		}
	}
	if err != nil {
		return zero, err
	}
	if _, err = s.RecordPublicationIntent(ctx, w.attempt, datapublish.PublishStage); err != nil {
		return zero, err
	}
	w.publication, err = acquireRestoredPublicationStage(ctx, working, podman.New(w.operations.runtime.session))
	if err != nil {
		return zero, err
	}
	action := moduleStagePublish
	if w.opened.restored.source.Kind() == "unpublished" {
		action = moduleStageCreate
	}
	evidence, err := runRestoredPublicationStage(ctx, w.publication, s, w.attempt, action, localexec.ExecRunner{}, w.operations.runtime.session)
	if err != nil {
		return zero, err
	}
	checkpoint, err := sealPublicationTarget(ctx, evidence)
	if err != nil {
		return zero, err
	}
	if _, err = s.RecordPublicationIntent(ctx, w.attempt, datapublish.VerifyStage); err != nil {
		return zero, err
	}
	if _, err = verifyPublicationTarget(ctx, checkpoint, w.operations.runtime.session); err != nil {
		return zero, err
	}
	if _, err = s.RecordPublicationIntent(ctx, w.attempt, datapublish.DisposeStage); err != nil {
		return zero, err
	}
	if _, err = s.RecordPublicationDisposed(ctx, w.attempt); err != nil {
		return zero, err
	}
	return checkpoint, nil
}

func (w *publicationApplyWork) transition(ctx context.Context, checkpoint publicationTargetCheckpoint) (datapublish.TaskReceipt, error) {
	zero := datapublish.TaskReceipt{}
	s := w.operations.service.Store
	preparation, err := s.BeginPublicationTargetPreparation(ctx, w.attempt, w.opened.lifetime)
	if err != nil {
		return zero, err
	}
	w.target, err = preparePublicationTarget(ctx, s, preparation, checkpoint, w.operations.runtime.home)
	if err != nil {
		return zero, err
	}
	incoming, err := s.ClaimPublicationIncomingRepository(ctx, w.attempt)
	if err != nil {
		return zero, err
	}
	initialized, err := initializePublicationIncoming(ctx, w.operations.runtime, incoming, w.target.facts.TransitionID)
	if err != nil {
		return zero, err
	}
	transition, err := s.ClaimPublicationTransition(ctx, preparation, incoming, initialized)
	if err != nil {
		return zero, err
	}
	view, err := s.ReadPublicationTransition(ctx, transition)
	if err != nil {
		return zero, err
	}
	tree := &engineTransitionTree{preparation: w.opened.preparation, lifetime: w.opened.lifetime, binding: w.target.facts.Binding, input: view.Input, limits: checkpoint.stage.source.limits}
	if err = relocatePublicationTree(ctx, s, transition, tree); err != nil {
		return zero, err
	}
	observed, err := w.observeTransition(ctx, tree, view.Runtime)
	if err != nil {
		return zero, err
	}
	if _, err = s.ActivatePublicationTransition(ctx, transition, observed); err != nil {
		return zero, err
	}
	observed, err = w.observeTransition(ctx, tree, view.Runtime)
	if err != nil {
		return zero, err
	}
	if _, err = s.BeginPublicationTransitionCompletion(ctx, transition, observed); err != nil {
		return zero, err
	}
	observed, err = w.observeTransition(ctx, tree, view.Runtime)
	if err != nil {
		return zero, err
	}
	if _, err = s.CompletePublicationTransition(ctx, transition, observed); err != nil {
		return zero, err
	}
	return s.PublicationCompletionReceipt(ctx, transition)
}

func (w *publicationApplyWork) observeTransition(ctx context.Context, tree *engineTransitionTree, runtime data.EngineRuntime) (store.PublicationTransitionObservation, error) {
	zero := store.PublicationTransitionObservation{}
	if _, err := w.operations.runtime.inspectPreparedStop(ctx, w.op.App, w.opened.restored.permit, w.stop, w.routes, w.withdrawn, w.previous); err != nil {
		return zero, err
	}
	if !publicationReviewProcessesKnown(nil, w.preflight) || !publicationReviewProcessesKnown(nil, w.publication) {
		return zero, datapublish.ErrInspection
	}
	layout, err := tree.inspect(ctx)
	if err != nil {
		return zero, err
	}
	unit, err := readPublicationUnit(ctx, w.operations.runtime.home, runtime, w.opened.restored.permit.Runtime.State == data.EngineUnpublished)
	if err != nil {
		return zero, err
	}
	permit := w.opened.restored.permit
	permit.Runtime = runtime.Clone()
	material, err := readEngineMaterial(ctx, w.opened.preparation.StateRoot(), permit)
	material.clear()
	if err != nil {
		return zero, err
	}
	raw, _ := json.Marshal(runtime.Secrets)
	return store.PublicationTransitionObservation{Layout: layout, UnitSHA256: unit, UnitAbsent: unit == "", MaterialSHA256: nativeBackupBytesHash(raw), EngineAbsent: true, ApplicationAbsent: true, IngressClosed: true, PrivateReadersAbsent: true, ObservedAt: time.Now().UTC()}, nil
}

func (w *publicationApplyWork) close() error {
	if w == nil {
		return nil
	}
	if !publicationReviewProcessesKnown(nil, w.preflight) || !publicationReviewProcessesKnown(nil, w.publication) {
		return nil
	}
	for _, lease := range []*publicationStageLease{w.preflight, w.publication} {
		if lease != nil {
			lease.material.clear()
		}
	}
	var err error
	if w.target != nil {
		err = errors.Join(err, w.target.close())
	}
	if w.artifact != nil {
		err = errors.Join(err, w.artifact.discard())
	}
	if w.opened != nil {
		err = errors.Join(err, w.opened.close())
	}
	return err
}
