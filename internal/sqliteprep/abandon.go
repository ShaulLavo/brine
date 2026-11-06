package sqliteprep

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

// AbandonStop is one abandonment's durable intent to settle a live predecessor
// to stopped. It is recorded before any stop and binds the real task, input,
// predecessor intent, original held fence and current policy. It authorizes
// only the recorded stops, never a start, restart or remote request.
type AbandonStop struct {
	Task         Task                 `json:"task"`
	Input        Input                `json:"input"`
	IntentSHA256 string               `json:"intent_sha256"`
	Fence        data.QuiescenceFence `json:"fence"`
	PolicySHA256 string               `json:"policy_sha256"`
	StopWriters  bool                 `json:"stop_writers"`
	StopReplica  bool                 `json:"stop_replica"`
	ClaimedAt    time.Time            `json:"claimed_at"`
}

func (s AbandonStop) Validate() error {
	f := s.Fence
	if !s.Input.Matches(s.Task) || s.Task.Kind != AbandonTask || !data.ValidCatalogHash(s.IntentSHA256) || !data.ValidCatalogHash(s.PolicySHA256) || !s.StopWriters && !s.StopReplica || !utc(s.ClaimedAt) {
		return ErrInvalid
	}
	if !data.ValidID(string(f.ID)) || !token.MatchString(f.OperationID) || f.DatabaseID != s.Input.DatabaseID || !data.ValidID(string(f.IncarnationID)) || f.State != data.FenceHeld {
		return ErrInvalid
	}
	return nil
}

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
		return w.settleStop(ctx, err)
	}
	receipt, err := s.Journal.CompleteAbandon(ctx, t, in, a, w.held)
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			return w.settleStop(ctx, err)
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

// settleStop never fails a task whose stop intent was recorded; an unknown
// claim result is read back rather than guessed.
func (w *work) settleStop(ctx context.Context, cause error) (Result, error) {
	if w.stop != nil {
		return w.settle(ctx, cause, true)
	}
	return w.settleClaim(ctx, cause)
}

func (w *work) abandonObservation(ctx context.Context) (Abandonment, error) {
	l := w.facts.Lineage
	origins, err := ResolveOrigins(*l)
	if err != nil {
		return Abandonment{}, err
	}
	if err = w.settleStopped(ctx, origins); err != nil {
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
	return Abandonment{Intent: l.Previous, Point: w.facts.Point, Stop: w.stop, Stopped: final, OldObject: state, ObservedAt: w.service.now()}, nil
}

// settleStopped proves the predecessor already stopped, or durably records a
// stop intent and then stops only the freshly observed owned units it names.
// It reads no remote state or credential and never starts or retries.
func (w *work) settleStopped(ctx context.Context, origins Origins) error {
	o, err := w.observeOwned(ctx)
	if err != nil {
		return err
	}
	if o.WritersStopped && (o.Replica == Stopped || o.Replica == Missing) {
		return w.runtime(ctx, false)
	}
	stop, err := w.abandonStop(o, origins)
	if err != nil {
		return err
	}
	if err = w.service.Journal.ClaimAbandon(ctx, stop, w.guard); err != nil {
		return err
	}
	w.stop = &stop
	if err = w.stopOwned(ctx, stop); err != nil {
		return err
	}
	return w.runtime(ctx, false)
}

// abandonStop admits stops only for a live lineage without a stopped
// baseline. After a baseline, a running unit may have changed the source.
func (w *work) abandonStop(o Observation, origins Origins) (AbandonStop, error) {
	scope := w.facts.Scope
	running := o.Replica == Running
	if !running && o.Replica != Stopped && o.Replica != Missing || running && !scope.ReplicaCommitted {
		return AbandonStop{}, refuse(LocalUncertain)
	}
	if origins.Baseline != nil {
		return AbandonStop{}, refuse(StoppedSourceChanged)
	}
	digest, err := Fingerprint(w.facts.Lineage.Previous)
	if err != nil {
		return AbandonStop{}, err
	}
	stop := AbandonStop{Task: w.task, Input: w.input, IntentSHA256: digest, Fence: w.facts.Fence, PolicySHA256: scope.PolicySHA256, StopWriters: !o.WritersStopped, StopReplica: running, ClaimedAt: w.service.now()}
	return stop, stop.Validate()
}

// stopOwned issues only the recorded stops, writers first, each after a fresh
// owned observation. A replica stop also requires stopped writers.
func (w *work) stopOwned(ctx context.Context, stop AbandonStop) error {
	previous := w.facts.Lineage.Previous
	if stop.StopWriters {
		if _, err := w.observeOwned(ctx); err != nil {
			return err
		}
		if err := w.guard.StopWriters(ctx, previous); err != nil {
			return err
		}
	}
	if !stop.StopReplica {
		return nil
	}
	o, err := w.observeOwned(ctx)
	if err != nil {
		return err
	}
	if !o.WritersStopped {
		return refuse(LocalUncertain)
	}
	return w.guard.StopReplica(ctx, previous)
}

func (w *work) observeOwned(ctx context.Context) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	o, err := w.guard.Observe(ctx)
	if err != nil {
		return Observation{}, err
	}
	s := w.facts.Scope
	if !o.Owned || !o.JobsAbsent || !validSchema(o.Schema, s.DatabaseID) || !sameSchema(o.Schema, s.Schema) || o.Schema.ObservedAt.After(w.service.now()) {
		return Observation{}, refuse(LocalUncertain)
	}
	return o, nil
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
