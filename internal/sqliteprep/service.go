package sqliteprep

import (
	"context"
	"errors"
	"time"
)

const PreparationBudget = 20 * time.Minute
const PointLifetime = 10 * time.Minute
const journalBudget = 5 * time.Second

type Service struct {
	Journal Journal
	Host    Host
	Now     func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type work struct {
	service   Service
	task      Task
	input     Input
	facts     Facts
	guard     HostGuard
	held      StoppedSource
	history   History
	selection Selection
	baseline  StoppedBundle
	source    RemoteSource
	expected  *PreparedPoint
	stop      *AbandonStop
}

func (w *work) close(r *Result, err *error) {
	var cleanup error
	if w.held != nil {
		cleanup = w.held.Close()
	}
	if w.guard != nil {
		cleanup = errors.Join(cleanup, w.guard.Close())
	}
	if r.Receipt.Validate() == nil {
		*r = Result{Receipt: r.Receipt, CleanupError: errors.Join(r.CleanupError, cleanup)}
		return
	}
	*err = errors.Join(*err, cleanup)
}

func (s Service) Prepare(ctx context.Context, t Task, in Input) (r Result, err error) {
	ctx, cancel := context.WithTimeout(ctx, PreparationBudget)
	defer cancel()
	w := work{service: s, task: t, input: in}
	defer w.close(&r, &err)
	if t.Kind != PrepareTask {
		return r, ErrInvalid
	}
	ready, err := w.begin(ctx)
	if err != nil || ready != nil {
		if ready != nil {
			return *ready, err
		}
		return r, err
	}
	credentialCtx, credentialCancel := context.WithTimeout(ctx, w.facts.CredentialExpiresAt.Sub(s.now()))
	defer credentialCancel()
	ctx = credentialCtx
	if err = w.admit(ctx); err != nil {
		return w.settle(ctx, err, false)
	}
	if err = w.service.Journal.Claim(ctx, w.history.Intent, w.guard, w.held); err != nil {
		return w.settleClaim(ctx, err)
	}
	w.history.Events = []Event{{PreparationID: in.PreparationID, Sequence: 1, Stage: Claimed, At: w.history.Intent.ClaimedAt}}
	if err = w.prepare(ctx); err != nil {
		return w.settle(ctx, err, true)
	}
	return w.complete(ctx)
}

// begin reads a joined historical result before any host effects. Failed,
// recovery-required and already-claimed invocations are never automatically run.
func (w *work) begin(ctx context.Context) (*Result, error) {
	if w.service.Journal == nil || w.service.Host == nil || !w.input.Matches(w.task) {
		return nil, ErrInvalid
	}
	status, err := w.service.Journal.ReadTask(ctx, w.task)
	if err != nil {
		return nil, err
	}
	ready, err := w.readStatus(status)
	if err != nil || ready != nil {
		return ready, err
	}
	w.facts, err = w.service.Journal.Inspect(ctx, w.task, w.input)
	if err != nil {
		_, settled := w.settle(ctx, err, false)
		return nil, settled
	}
	if err = w.validateFacts(); err != nil {
		_, settled := w.settle(ctx, err, false)
		return nil, settled
	}
	w.guard, err = w.service.Host.Acquire(ctx, w.input, w.facts.Scope, w.facts.Fence)
	if err != nil {
		_, settled := w.settle(ctx, err, false)
		return nil, settled
	}
	if w.guard == nil {
		_, settled := w.settle(ctx, ErrInvalid, false)
		return nil, settled
	}
	return nil, nil
}
func (w *work) validateFacts() error {
	f := w.facts
	if f.Scope.Validate() != nil || f.Scope.App != w.task.App || f.Scope.DatabaseID != w.input.DatabaseID || !validFence(f.Fence, f.Scope) || f.Location.Validate() != nil {
		return ErrInvalid
	}
	if w.task.Kind == PrepareTask && w.input.Supersedes == "" {
		if f.Lineage != nil || f.Point != nil {
			return ErrInvalid
		}
		return nil
	}
	l := f.Lineage
	if l == nil || l.Previous.Validate() != nil || !l.Terminal || !l.Unconsumed || l.Previous.Task.Requester != w.task.Requester || l.Previous.Fence != f.Fence || l.Previous.Location != f.Location || !sameSourceScope(l.Previous.Scope, f.Scope) || w.task.Kind == PrepareTask && !sameLineageScope(l.Previous.Scope, f.Scope) {
		return ErrInvalid
	}
	wanted := w.input.Supersedes
	if w.task.Kind == AbandonTask {
		wanted = w.input.PreparationID
	}
	if l.Previous.Input.PreparationID != wanted {
		return ErrInvalid
	}
	if f.UncertainSnapshot != nil && !validUncertainSnapshot(l.Previous, *f.UncertainSnapshot) {
		return ErrInvalid
	}
	if f.Point != nil {
		digest, err := Fingerprint(l.Previous)
		if err != nil || f.Point.Validate() != nil || f.Point.PreparationID != wanted || f.Point.IntentSHA256 != digest {
			return ErrInvalid
		}
	}
	return nil
}
func (w *work) readStatus(status TaskStatus) (*Result, error) {
	if status.Task != w.task {
		return nil, ErrInvalid
	}
	if status.State == Succeeded {
		if status.Receipt == nil || !w.receiptMatches(*status.Receipt) {
			return nil, ErrInvalid
		}
		return &Result{Receipt: *status.Receipt}, nil
	}
	if status.Receipt != nil {
		return nil, ErrInvalid
	}
	if status.State != Preflight || status.Claimed {
		return nil, ErrRecovery
	}
	return nil, nil
}
func (w *work) receiptMatches(r Receipt) bool {
	if r.Validate() != nil || r.Task != w.task || r.PreparationID != w.input.PreparationID || r.Fence.DatabaseID != w.input.DatabaseID {
		return false
	}
	if w.facts.Fence.ID != "" {
		expected := w.facts.Fence
		if w.task.Kind == AbandonTask {
			expected.State = "released"
		}
		if r.Fence != expected {
			return false
		}
	}
	if w.expected != nil {
		digest, err := Fingerprint(*w.expected)
		if err != nil || r.PreparedSHA256 != digest || r.SourcePointID != w.expected.Source.Point.ID || r.CompletedAt.Before(w.expected.PreparedAt) {
			return false
		}
	}
	if w.task.Kind == AbandonTask && w.facts.Lineage != nil {
		return w.abandonReceiptMatches(r)
	}
	return true
}
func (w *work) abandonReceiptMatches(r Receipt) bool {
	if w.facts.Point == nil {
		return r.PreparedSHA256 == "" && r.SourcePointID == ""
	}
	digest, err := Fingerprint(*w.facts.Point)
	return err == nil && r.PreparedSHA256 == digest && r.SourcePointID == w.facts.Point.Source.Point.ID
}

func (w *work) admit(ctx context.Context) error {
	now := w.service.now()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !utc(w.facts.CredentialExpiresAt) || !now.Before(w.facts.CredentialExpiresAt) {
		return refuse(CredentialsExpired)
	}
	o, err := w.guard.Observe(ctx)
	if err != nil {
		return err
	}
	var bundle *StoppedBundle
	if o.Replica == Stopped || o.Replica == Missing {
		w.held, err = w.guard.HoldStopped(ctx)
		if err != nil {
			return err
		}
		b, err := w.stable(ctx)
		if err != nil {
			return err
		}
		bundle = &b
	}
	w.selection, err = Select(w.facts.Scope, w.facts.Lineage, o, bundle, w.service.now())
	if err != nil {
		return err
	}
	sel := w.selection
	now = w.service.now()
	i := Intent{Task: w.task, Input: w.input, Scope: w.facts.Scope, Fence: w.facts.Fence, Location: w.facts.Location, Mode: sel.Mode, Arm: sel.Arm, StopWritersRequired: sel.StopWritersRequired, CoverageOrigin: sel.CoverageOrigin, BaselineFrom: sel.BaselineFrom, ClaimedAt: now}
	if err = i.Validate(); err != nil {
		return err
	}
	w.history.Intent = i
	return nil
}
func (w *work) prepare(ctx context.Context) error {
	i := w.history.Intent
	if i.StopWritersRequired {
		if err := w.append(ctx, Event{Stage: WritersStopIntent}); err != nil {
			return err
		}
		if err := w.guard.StopWriters(ctx, i); err != nil {
			return err
		}
	}
	running := i.Arm == LTX && i.Mode != SupersedeReverify
	if err := w.runtime(ctx, running); err != nil {
		return err
	}
	if err := w.append(ctx, Event{Stage: WritersStopped}); err != nil {
		return err
	}
	if running {
		return w.live(ctx)
	}
	return w.stopped(ctx)
}
func (w *work) append(ctx context.Context, e Event) error {
	e.PreparationID = w.input.PreparationID
	e.Sequence = len(w.history.Events) + 1
	e.At = w.service.now()
	candidate := History{Intent: w.history.Intent, Events: append(w.history.Events[:len(w.history.Events):len(w.history.Events)], e)}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := w.service.Journal.Append(ctx, w.history.Intent, e); err != nil {
		return err
	}
	w.history = candidate
	return nil
}
func (w *work) cutoff() time.Time {
	for _, e := range w.history.Events {
		if e.Stage == WritersStopped {
			return e.At
		}
	}
	return time.Time{}
}
func (w *work) live(ctx context.Context) error {
	var err error
	w.source, err = w.guard.Barrier(ctx, w.history.Intent, w.cutoff())
	if err != nil {
		return err
	}
	if err = w.append(ctx, Event{Stage: CoverageUploaded, Source: &w.source}); err != nil {
		return err
	}
	if err = w.verify(ctx); err != nil {
		return err
	}
	if err = w.runtime(ctx, true); err != nil {
		return err
	}
	if err = w.append(ctx, Event{Stage: ReplicaStopIntent}); err != nil {
		return err
	}
	if err = w.guard.StopReplica(ctx, w.history.Intent); err != nil {
		return err
	}
	return w.recordStopped(ctx)
}
func (w *work) stopped(ctx context.Context) error {
	if err := w.recordStopped(ctx); err != nil {
		return err
	}
	if w.history.Intent.Mode == SupersedeReverify {
		w.source = *w.selection.Source
		if err := w.append(ctx, Event{Stage: CoverageRetained, Source: &w.source}); err != nil {
			return err
		}
		return w.verify(ctx)
	}
	if !w.baseline.CaptureFits() {
		return refuse(CoverageUnavailable)
	}
	snapshot, err := w.guard.Capture(ctx, w.history.Intent, w.held, w.cutoff())
	if err != nil {
		return err
	}
	if snapshot.Validate() != nil || snapshot.Point.ID != w.input.PreparationID || snapshot.Point.Snapshot.ObjectKey != SnapshotKey(w.facts.Scope, w.input.PreparationID) || !snapshot.Cutoff.Equal(w.cutoff()) {
		return ErrInvalid
	}
	if err = w.append(ctx, Event{Stage: CoverageIntent, Snapshot: &snapshot}); err != nil {
		return err
	}
	w.source, err = w.guard.Upload(ctx, w.history.Intent, snapshot)
	if err != nil {
		return err
	}
	if err = w.append(ctx, Event{Stage: CoverageUploaded, Source: &w.source}); err != nil {
		return err
	}
	return w.verify(ctx)
}
func (w *work) recordStopped(ctx context.Context) error {
	if err := w.runtime(ctx, false); err != nil {
		return err
	}
	if w.held == nil {
		var err error
		w.held, err = w.guard.HoldStopped(ctx)
		if err != nil {
			return err
		}
	}
	bundle, err := w.stable(ctx)
	if err != nil {
		return err
	}
	if w.selection.Baseline != nil && !w.selection.Baseline.Matches(bundle) {
		return refuse(StoppedSourceChanged)
	}
	w.baseline = bundle
	return w.append(ctx, Event{Stage: ReplicaStopped, Stopped: &bundle})
}
func (w *work) stable(ctx context.Context) (StoppedBundle, error) {
	if w.held == nil {
		return StoppedBundle{}, refuse(LocalUncertain)
	}
	before, err := w.held.Recheck(ctx)
	if err != nil {
		return StoppedBundle{}, err
	}
	schema, err := w.held.Schema(ctx)
	if err != nil || !validSchema(schema, w.facts.Scope.DatabaseID) || !sameSchema(schema, w.facts.Scope.Schema) || schema.ObservedAt.After(w.service.now()) {
		return StoppedBundle{}, refuse(LocalUncertain)
	}
	after, err := w.held.Recheck(ctx)
	if err != nil {
		return StoppedBundle{}, err
	}
	if !before.Matches(after) || after.Location != w.facts.Location {
		return StoppedBundle{}, refuse(StoppedSourceChanged)
	}
	if w.facts.Scope.EmptyAllocation != nil && after.DB.Presence != Absent {
		return StoppedBundle{}, refuse(StoppedSourceChanged)
	}
	if w.facts.Scope.EmptyAllocation == nil && after.DB.Presence != Regular {
		return StoppedBundle{}, refuse(LocalUncertain)
	}
	return after, nil
}
func (w *work) runtime(ctx context.Context, running bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.task.Kind == PrepareTask && !w.service.now().Before(w.facts.CredentialExpiresAt) {
		return refuse(CredentialsExpired)
	}
	o, err := w.guard.Observe(ctx)
	if err != nil {
		return err
	}
	replicaMatches := o.Replica == Running
	if !running {
		replicaMatches = o.Replica == Stopped || o.Replica == Missing
	}
	if !o.WritersStopped || !o.JobsAbsent || !o.Owned || !replicaMatches || !validSchema(o.Schema, w.facts.Scope.DatabaseID) || !sameSchema(o.Schema, w.facts.Scope.Schema) || o.Schema.ObservedAt.After(w.service.now()) {
		return refuse(LocalUncertain)
	}
	return nil
}
func (w *work) verify(ctx context.Context) error {
	r, err := w.guard.Verify(ctx, w.history.Intent, w.source)
	if err != nil {
		return err
	}
	if !verificationMatches(w.history.Intent, w.source, r) {
		return ErrInvalid
	}
	return w.append(ctx, Event{Stage: Verified, Verification: &r})
}
func (w *work) complete(ctx context.Context) (Result, error) {
	if err := w.runtime(ctx, false); err != nil {
		return w.settle(ctx, err, true)
	}
	bundle, err := w.stable(ctx)
	if err != nil {
		return w.settle(ctx, err, true)
	}
	if !w.baseline.Matches(bundle) {
		return w.settle(ctx, refuse(StoppedSourceChanged), true)
	}
	i := w.history.Intent
	digest, err := Fingerprint(i)
	if err != nil {
		return w.settle(ctx, err, true)
	}
	p := PreparedPoint{PreparationID: w.input.PreparationID, IntentSHA256: digest, Source: w.source, Stopped: bundle, PreparedAt: w.service.now()}
	p.ExpiresAt = p.PreparedAt.Add(PointLifetime)
	if p.ExpiresAt.After(i.Scope.Retention.RetainUntil) {
		p.ExpiresAt = i.Scope.Retention.RetainUntil
	}
	for _, e := range w.history.Events {
		if e.Verification != nil {
			p.Verification = *e.Verification
		}
	}
	if err = ValidatePrepared(w.history, p); err != nil {
		return w.settle(ctx, err, true)
	}
	w.expected = &p
	receipt, err := w.service.Journal.CompletePrepare(ctx, w.history, p, w.held)
	if err != nil {
		return w.settle(ctx, err, true)
	}
	pointHash, err := Fingerprint(p)
	if err != nil || !w.receiptMatches(receipt) || receipt.PreparedSHA256 != pointHash || receipt.SourcePointID != p.Source.Point.ID || receipt.Fence != i.Fence || receipt.CompletedAt.Before(p.PreparedAt) {
		return w.settle(ctx, ErrInvalid, true)
	}
	return Result{Receipt: receipt}, nil
}

func (w *work) settleClaim(ctx context.Context, cause error) (Result, error) {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalBudget)
	defer cancel()
	status, err := w.service.Journal.ReadTask(journal, w.task)
	if err != nil || status.Task != w.task {
		return Result{}, errors.Join(ErrRecovery, cause, err)
	}
	if status.Claimed {
		return w.settle(ctx, cause, true)
	}
	return w.settle(ctx, cause, false)
}
func (w *work) settle(ctx context.Context, cause error, claimed bool) (Result, error) {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalBudget)
	defer cancel()
	status, err := w.service.Journal.ReadTask(journal, w.task)
	if err != nil || status.Task != w.task {
		return Result{}, errors.Join(ErrRecovery, cause, err)
	}
	if status.State == Succeeded {
		ready, err := w.readStatus(status)
		if err != nil {
			return Result{}, errors.Join(ErrRecovery, cause, err)
		}
		return *ready, nil
	}
	if status.State != Preflight {
		return Result{}, errors.Join(ErrRecovery, cause)
	}
	if claimed || status.Claimed {
		err = w.service.Journal.MarkRecovery(journal, w.task, EffectUnknown)
		return Result{}, errors.Join(ErrRecovery, cause, err)
	}
	var refusal *Refusal
	code := InvalidEvidence
	if errors.As(cause, &refusal) {
		code = refusal.Code
	}
	err = w.service.Journal.FailUnclaimed(journal, w.task, code)
	return Result{}, errors.Join(cause, err)
}

func validUncertainSnapshot(i Intent, p SnapshotPlan) bool {
	return p.Validate() == nil && p.Point.ID == i.Input.PreparationID && p.Point.BindingID == i.Scope.BindingID && p.Point.EpochID == i.Scope.EpochID && sameSchema(p.Point.Schema, pointSchema(i.Scope.Schema)) && p.Point.Snapshot.ObjectKey == SnapshotKey(i.Scope, i.Input.PreparationID) && !p.Cutoff.Before(i.ClaimedAt)
}
