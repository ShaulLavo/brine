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
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationResolveWork struct {
	operations  modulePublications
	op          ops.Operation
	facts       store.PublicationRecoveryFacts
	preparation *store.EnginePreparation
	lifetime    *data.EngineLock
	tree        *engineTransitionTree
	routes      *caddy.Manager
	withdrawn   caddy.State
	previous    caddy.State
	stop        store.NativeStopObservation
}

func modulePublicationResolveTask(operations modulePublications) func(context.Context, ops.Operation) (json.RawMessage, error) {
	return func(ctx context.Context, op ops.Operation) (_ json.RawMessage, err error) {
		if op.Kind != ops.ModulePublishResolve || op.State != ops.Preflight || operations.checkReviewedPlan(ctx, op.App, op.SecretRef) != nil {
			return nil, datapublish.ErrRecord
		}
		lock, err := operations.service.Store.AcquireHostLock(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, lock.Release()) }()
		work := &publicationResolveWork{operations: operations, op: op}
		defer func() { err = errors.Join(err, work.close()) }()
		if err = work.open(ctx); err != nil {
			return nil, err
		}
		observed, err := work.observe(ctx)
		if err != nil {
			return nil, err
		}
		s := operations.service.Store
		transition, err := s.ResumePublicationTransition(ctx, work.preparation, work.lifetime, op.SecretRef, op.RecoveryOf, op.ID, observed)
		if err != nil {
			return nil, err
		}
		if err = work.complete(ctx, transition); err != nil {
			return nil, err
		}
		receipt, err := s.PublicationCompletionReceipt(ctx, transition)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
}

func (w *publicationResolveWork) open(ctx context.Context) error {
	s := w.operations.service.Store
	facts, err := s.PublicationTransitionRecoveryFacts(ctx, w.op.SecretRef, w.op.RecoveryOf, w.op.ID)
	if err != nil {
		return err
	}
	w.facts = facts
	w.preparation, err = s.AcquireEnginePreparation(ctx, facts.Point.BindingID, facts.Point.FenceID, facts.Point.FenceOwnerOpID)
	if err != nil {
		return err
	}
	w.lifetime, err = data.AcquireEngineLifetimeLock(ctx, w.operations.runtime.stateRoot, facts.Point.BindingID)
	if err != nil {
		return err
	}
	w.tree = &engineTransitionTree{preparation: w.preparation, lifetime: w.lifetime, binding: facts.Binding, input: facts.Input, limits: engineCopyLimits{Bytes: facts.SourcePermit.Runtime.CopyBytes, Files: facts.SourcePermit.Runtime.CopyFiles, Depth: 16}}
	_, w.stop, _, err = s.ReadNativePreparedStopExpectation(ctx, facts.Point.PointID)
	if err != nil {
		return err
	}
	w.routes, w.withdrawn, w.previous, err = w.operations.runtime.preparedRouteStates(ctx, w.operations.service, facts.Point.PointID, w.op.App, facts.DeclarationID)
	return err
}

func (w *publicationResolveWork) observe(ctx context.Context) (store.PublicationTransitionObservation, error) {
	zero := store.PublicationTransitionObservation{}
	if w == nil || w.tree == nil {
		return zero, datapublish.ErrRecord
	}
	if _, err := w.operations.runtime.inspectPreparedStop(ctx, w.op.App, w.facts.SourcePermit, w.stop, w.routes, w.withdrawn, w.previous); err != nil {
		return zero, err
	}
	layout, err := w.tree.inspect(ctx)
	if err != nil {
		return zero, err
	}
	unit, err := readNativeRestoreUnit(ctx, w.operations.runtime.home, w.facts.TargetRuntime)
	if err != nil {
		return zero, err
	}
	permit := w.facts.SourcePermit
	permit.Runtime = w.facts.TargetRuntime.Clone()
	material, err := readEngineMaterial(ctx, w.operations.runtime.stateRoot, permit)
	material.clear()
	if err != nil {
		return zero, err
	}
	raw, _ := json.Marshal(w.facts.TargetRuntime.Secrets)
	return store.PublicationTransitionObservation{Layout: layout, UnitSHA256: unit, MaterialSHA256: nativeBackupBytesHash(raw), EngineAbsent: true, ApplicationAbsent: true, IngressClosed: true, PrivateReadersAbsent: true, ObservedAt: time.Now().UTC()}, nil
}

func (w *publicationResolveWork) complete(ctx context.Context, transition *store.PublicationTransition) error {
	s := w.operations.service.Store
	view, err := s.ReadPublicationTransition(ctx, transition)
	if err != nil {
		return err
	}
	if view.Cursor.Stage != data.EngineTransitionActivated && view.Cursor.Stage != data.EngineTransitionFinishIntent {
		if err = relocatePublicationTree(ctx, s, transition, w.tree); err != nil {
			return err
		}
		observed, err := w.observe(ctx)
		if err != nil {
			return err
		}
		if _, err = s.ActivatePublicationTransition(ctx, transition, observed); err != nil {
			return err
		}
	}
	view, err = s.ReadPublicationTransition(ctx, transition)
	if err != nil {
		return err
	}
	if view.Cursor.Stage == data.EngineTransitionActivated {
		observed, err := w.observe(ctx)
		if err != nil {
			return err
		}
		if _, err = s.BeginPublicationTransitionCompletion(ctx, transition, observed); err != nil {
			return err
		}
	}
	observed, err := w.observe(ctx)
	if err != nil {
		return err
	}
	_, err = s.CompletePublicationTransition(ctx, transition, observed)
	return err
}

func (w *publicationResolveWork) close() error {
	if w == nil {
		return nil
	}
	var err error
	if w.lifetime != nil {
		err = errors.Join(err, w.lifetime.Release())
	}
	if w.preparation != nil {
		err = errors.Join(err, w.preparation.Close())
	}
	return err
}
