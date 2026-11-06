package sqliteprep

import (
	"context"
	"errors"
	"time"
)

func (s Service) Abandon(ctx context.Context, t Task, in Input) (r Result, err error) {
	ctx, cancel := context.WithTimeout(ctx, PreparationBudget)
	defer cancel()
	w := work{service: s, task: t, input: in}
	defer w.close(&r, &err)
	if t.Kind != AbandonTask {
		return r, ErrInvalid
	}
	ready, err := w.begin(ctx)
	if err != nil || ready != nil {
		if ready != nil {
			return *ready, err
		}
		return r, err
	}
	a, err := w.abandonObservation(ctx)
	if err != nil {
		return w.settle(ctx, err, false)
	}
	receipt, err := s.Journal.CompleteAbandon(ctx, t, in, a, w.held)
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			return w.settleClaim(ctx, err)
		}
		return w.settle(ctx, err, true)
	}
	fence := a.Intent.Fence
	fence.State = "released"
	if !w.receiptMatches(receipt) || receipt.Fence != fence {
		return w.settle(ctx, ErrInvalid, true)
	}
	return Result{Receipt: receipt}, nil
}
func (w *work) abandonObservation(ctx context.Context) (Abandonment, error) {
	l := w.facts.Lineage
	origins, err := ResolveOrigins(*l)
	if err != nil {
		return Abandonment{}, err
	}
	if err = w.runtime(ctx, false); err != nil {
		return Abandonment{}, err
	}
	w.held, err = w.guard.HoldStopped(ctx)
	if err != nil {
		return Abandonment{}, err
	}
	bundle, err := w.stable(ctx)
	if err != nil {
		return Abandonment{}, err
	}
	if origins.Baseline != nil && !origins.Baseline.Matches(bundle) {
		return Abandonment{}, refuse(StoppedSourceChanged)
	}
	state := ObjectUnknown
	if w.facts.UncertainSnapshot != nil {
		state = w.oldObject(ctx, *w.facts.UncertainSnapshot)
	}
	// An informational remote read cannot substitute for the final local recheck.
	if err = w.runtime(ctx, false); err != nil {
		return Abandonment{}, err
	}
	final, err := w.stable(ctx)
	if err != nil {
		return Abandonment{}, err
	}
	if !bundle.Matches(final) {
		return Abandonment{}, refuse(StoppedSourceChanged)
	}
	return Abandonment{Intent: l.Previous, Point: w.facts.Point, Stopped: final, OldObject: state, ObservedAt: w.service.now()}, nil
}
func (w *work) oldObject(ctx context.Context, p SnapshotPlan) ObjectState {
	// Optional observations consume a child budget, never the local release's
	// whole deadline. Any error or invalid class stays informational unknown.
	child, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	state, err := w.guard.OldObject(child, p)
	if err != nil || state != ObjectPresent && state != ObjectAbsent {
		return ObjectUnknown
	}
	return state
}
