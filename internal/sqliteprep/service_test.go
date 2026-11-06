package sqliteprep

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

var errFault = errors.New("injected errFault")

type fakeHost struct {
	fence         data.QuiescenceFence
	scope         Scope
	obs           Observation
	bundle        StoppedBundle
	calls         []string
	fail          string
	held          bool
	closed        bool
	closeErr      bool
	oldState      ObjectState
	oldErr        error
	clock         time.Time
	crossSnapshot bool
	delayVerify   time.Duration
	changeOnCall  string
}

func (h *fakeHost) call(name string) error {
	h.calls = append(h.calls, name)
	h.clock = h.clock.Add(time.Second)
	if h.changeOnCall == name {
		h.bundle.WAL.Inode++
	}
	if h.fail == name {
		return errFault
	}
	return nil
}
func (h *fakeHost) Acquire(_ context.Context, s Scope, fence data.QuiescenceFence) (HostGuard, error) {
	h.scope = s
	h.fence = fence
	return h, h.call("acquire")
}
func (h *fakeHost) Observe(context.Context) (Observation, error) { return h.obs, h.call("observe") }
func (h *fakeHost) StopWriters(context.Context, Intent) error {
	h.obs.WritersStopped = true
	return h.call("stop_writers")
}
func (h *fakeHost) Barrier(_ context.Context, i Intent, cutoff time.Time) (RemoteSource, error) {
	s := fixtureSource(i)
	s.Cutoff = cutoff
	s.UploadedAt = h.clock.Add(time.Second)
	s.Point.RecordedAt = s.UploadedAt
	s.Point.LTX.Barrier.ObservedAt = s.UploadedAt
	return s, h.call("barrier")
}
func (h *fakeHost) StopReplica(context.Context, Intent) error {
	h.obs.Replica = Stopped
	return h.call("stop_replica")
}
func (h *fakeHost) HoldStopped(context.Context) (StoppedSource, error) {
	if err := h.call("hold"); err != nil {
		return nil, err
	}
	h.held = true
	return &fakeSource{host: h}, nil
}
func (h *fakeHost) Capture(_ context.Context, i Intent, _ StoppedSource, cutoff time.Time) (SnapshotPlan, error) {
	p := data.SQLiteRestorePoint{ID: i.Input.PreparationID, BindingID: i.Scope.BindingID, EpochID: i.Scope.EpochID, Kind: data.RestorePointSnapshot, Schema: pointSchema(i.Scope.Schema), Snapshot: &data.RestoreSnapshotPoint{ObjectKey: SnapshotKey(i.Scope, i.Input.PreparationID), SHA256: hash("2"), Size: 4096}, RecordedAt: h.clock.Add(time.Second)}
	if h.crossSnapshot {
		p.BindingID = data.ReplicaBindingID(id("9"))
	}
	return SnapshotPlan{Point: p, Cutoff: cutoff}, h.call("capture")
}
func (h *fakeHost) Upload(_ context.Context, i Intent, p SnapshotPlan) (RemoteSource, error) {
	s := RemoteSource{Origin: i.Input.PreparationID, Point: p.Point, Cutoff: p.Cutoff, UploadedAt: h.clock.Add(time.Second)}
	s.Point.RecordedAt = s.UploadedAt
	return s, h.call("upload")
}
func (h *fakeHost) Verify(_ context.Context, i Intent, s RemoteSource) (restore.SQLiteReceipt, error) {
	h.clock = h.clock.Add(h.delayVerify)
	err := h.call("verify")
	if h.fail == "contradict_replica" {
		h.obs.Replica = Running
	}
	return receiptFor(i, s, h.clock), err
}
func (h *fakeHost) OldObject(context.Context, SnapshotPlan) (ObjectState, error) {
	if err := h.call("old_head"); err != nil {
		return ObjectUnknown, err
	}
	return h.oldState, h.oldErr
}
func (h *fakeHost) Close() error {
	h.closed = true
	_ = h.call("close_host")
	if h.closeErr {
		return errFault
	}
	return nil
}

type fakeSource struct{ host *fakeHost }

func (g *fakeSource) Recheck(context.Context) (StoppedBundle, error) {
	if !g.host.held {
		return StoppedBundle{}, errFault
	}
	err := g.host.call("recheck")
	return g.host.bundle, err
}
func (g *fakeSource) Schema(context.Context) (data.SQLiteSchemaObservation, error) {
	return g.host.scope.Schema, g.host.call("schema")
}
func (g *fakeSource) Close() error { g.host.held = false; return g.host.call("close_source") }
func receiptFor(i Intent, s RemoteSource, at time.Time) restore.SQLiteReceipt {
	source := s.RestoreSource()
	schema := pointSchema(i.Scope.Schema)
	r := restore.SQLiteReceipt{OperationID: i.Task.OperationID, Source: source, ToolVersion: restore.SnapshotToolVersion, ObservedAt: at, Schema: restore.SQLiteSchemaObservation{State: restore.SchemaState(schema.State), Marker: schema.Marker, CatalogSHA256: schema.CatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "unknown", Coverage: &restore.CoverageEvidence{Source: source, CoveredThrough: s.Cutoff}, LossWindow: restore.LossWindow{State: restore.LossBounded, From: s.Cutoff, To: at, Reason: "Independent source-bound coverage proof bounds possible loss; later writes may be absent."}}
	if len(i.Scope.Checks) > 0 {
		r.InvariantCheck = "passed"
	}
	if source.LTX != nil {
		r.ToolVersion = restore.LitestreamVersion
		r.RequestedTXID = source.LTX.TXID
		r.RecoveredTXID = source.LTX.TXID
		r.PositionEvidence = "pinned_cli_exact_plan_and_successful_restore"
		r.Barrier = source.LTX.Barrier
	}
	return r
}

type fakeJournal struct {
	facts           Facts
	status          TaskStatus
	history         History
	point           *PreparedPoint
	fail            string
	completeCalls   int
	observedHeld    bool
	abandonObserved ObjectState
	calls           []string
}

func (j *fakeJournal) ReadTask(_ context.Context, t Task) (TaskStatus, error) {
	j.calls = append(j.calls, "read")
	if j.status.Task == (Task{}) {
		j.status = TaskStatus{Task: t, State: Preflight}
	}
	return j.status, nil
}
func (j *fakeJournal) Inspect(context.Context, Task, Input) (Facts, error) {
	j.calls = append(j.calls, "inspect")
	return j.facts, nil
}
func (j *fakeJournal) Claim(ctx context.Context, i Intent, observation HostObservation, g StoppedSource) error {
	o, err := observation.Observe(ctx)
	if err != nil {
		return err
	}
	var bundle *StoppedBundle
	if g != nil {
		b, err := g.Recheck(ctx)
		if err != nil {
			return err
		}
		bundle = &b
	}
	selection, err := Select(i.Scope, j.facts.Lineage, o, bundle, i.ClaimedAt)
	if err != nil || selection.Mode != i.Mode || selection.Arm != i.Arm {
		return ErrInvalid
	}
	j.calls = append(j.calls, "claim")
	if j.fail == "claim_before" {
		return errFault
	}
	j.history = History{Intent: i, Events: []Event{{PreparationID: i.Input.PreparationID, Sequence: 1, Stage: Claimed, At: i.ClaimedAt}}}
	j.status.Claimed = true
	if j.fail == "claim_after" {
		return errFault
	}
	return nil
}
func (j *fakeJournal) Append(_ context.Context, _ Intent, e Event) error {
	j.calls = append(j.calls, string(e.Stage))
	if j.fail == string(e.Stage) {
		return errFault
	}
	j.history.Events = append(j.history.Events, e)
	return j.history.Validate()
}
func (j *fakeJournal) CompletePrepare(ctx context.Context, h History, p PreparedPoint, g StoppedSource) (Receipt, error) {
	j.completeCalls++
	if j.fail == "complete_before" {
		return Receipt{}, errFault
	}
	b, err := g.Recheck(ctx)
	j.observedHeld = err == nil
	if err != nil || !b.Matches(p.Stopped) || ValidatePrepared(h, p) != nil {
		return Receipt{}, ErrInvalid
	}
	digest, _ := Fingerprint(p)
	r := Receipt{Task: h.Intent.Task, PreparationID: p.PreparationID, PreparedSHA256: digest, Fence: h.Intent.Fence, SourcePointID: p.Source.Point.ID, Outcome: PreparedOutcome, CompletedAt: p.PreparedAt}
	j.history.Events = append(j.history.Events, Event{PreparationID: p.PreparationID, Sequence: len(j.history.Events) + 1, Stage: Prepared, At: p.PreparedAt, PreparedSHA256: digest})
	if err := ValidateCompletion(j.history, p, r); err != nil {
		return Receipt{}, err
	}
	j.point = &p
	j.status.State = Succeeded
	j.status.Receipt = &r
	if j.fail == "complete_after" {
		return Receipt{}, errFault
	}
	return r, nil
}
func (j *fakeJournal) CompleteAbandon(ctx context.Context, t Task, in Input, a Abandonment, g StoppedSource) (Receipt, error) {
	j.completeCalls++
	if j.fail == "complete_before" {
		return Receipt{}, errFault
	}
	b, err := g.Recheck(ctx)
	j.observedHeld = err == nil
	if err != nil || !b.Matches(a.Stopped) {
		return Receipt{}, ErrInvalid
	}
	r := Receipt{Task: t, PreparationID: in.PreparationID, Fence: a.Intent.Fence, Outcome: AbandonedOutcome, OldObject: a.OldObject, StoppedDesiredRevision: 2, CompletedAt: a.ObservedAt}
	r.Fence.State = data.FenceReleased
	if a.Point != nil {
		r.SourcePointID = a.Point.Source.Point.ID
		r.PreparedSHA256, _ = Fingerprint(*a.Point)
	}
	j.abandonObserved = a.OldObject
	j.status.State = Succeeded
	j.status.Receipt = &r
	if j.fail == "complete_after" {
		return Receipt{}, errFault
	}
	return r, nil
}
func (j *fakeJournal) FailUnclaimed(_ context.Context, _ Task, _ Code) error {
	if j.status.Claimed {
		return ErrRecovery
	}
	if j.status.State != Preflight {
		return ErrInvalid
	}
	j.status.State = Failed
	return nil
}
func (j *fakeJournal) MarkRecovery(_ context.Context, _ Task, _ Code) error {
	if j.status.State == Succeeded {
		return ErrInvalid
	}
	j.status.State = RecoveryRequired
	return nil
}
func serviceFixture() (Service, *fakeJournal, *fakeHost, Task, Input) {
	i := fixtureIntent()
	h := &fakeHost{obs: fixtureObservation(), bundle: fixtureBundle(), clock: testNow.Add(time.Minute), oldState: ObjectUnknown}
	h.obs.Replica = Running
	h.obs.WritersStopped = false
	j := &fakeJournal{facts: Facts{Scope: i.Scope, Fence: i.Fence, Location: i.Location, CredentialExpiresAt: testNow.Add(time.Hour)}}
	s := Service{Journal: j, Host: h, Now: func() time.Time { return h.clock }}
	return s, j, h, i.Task, i.Input
}
func supersedeFixture() (Service, *fakeJournal, *fakeHost, Task, Input) {
	s, j, h, task, in := serviceFixture()
	l := fixtureLineage()
	j.facts.Lineage = &l
	h.obs = fixtureObservation()
	in.Supersedes = l.Previous.Input.PreparationID
	in.PreparationID = id("1")
	task.OperationID = "op-successor"
	task.InputSHA256 = mustHash(in)
	return s, j, h, task, in
}

func TestServiceFreshLTXAndStoppedSnapshot(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "ltx", false: "snapshot"}[running], func(t *testing.T) {
			checkServiceFreshLTXAndStoppedSnapshot(t, running)
		})
	}
}
func TestServiceReverifyKeepsOriginalSourceAndFence(t *testing.T) {
	s, j, h, task, in := supersedeFixture()
	original := j.facts.Lineage.Coverage.Source
	result, err := s.Prepare(t.Context(), task, in)
	if err != nil {
		t.Fatalf("%v host %v journal %v", err, h.calls, j.calls)
	}
	if !reflect.DeepEqual(j.point.Source, original) || !j.point.Verification.ObservedAt.After(original.UploadedAt) || result.Receipt.Fence.OperationID != j.facts.Fence.OperationID || h.fence.OperationID != j.facts.Fence.OperationID || result.Receipt.SourcePointID == in.PreparationID {
		t.Fatal("source or fence relabelled", j.point, result)
	}
	for _, effect := range []string{"barrier", "upload", "capture", "stop_replica", "stop_writers"} {
		if slices.Contains(h.calls, effect) {
			t.Fatal("unexpected", effect, h.calls)
		}
	}
	// A reverify of an inherited claim, before any own stopped/retained event.
	l := j.facts.Lineage
	l.Previous = j.history.Intent
	l.Previous.Input.Supersedes = original.Origin
	l.OwnStopped = false
	l.OwnUploaded = false
	j.status = TaskStatus{}
	j.point = nil
	in.Supersedes = in.PreparationID
	in.PreparationID = id("2")
	task.OperationID = "op-next"
	task.InputSHA256 = mustHash(in)
	if _, err := s.Prepare(t.Context(), task, in); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.point.Source, original) {
		t.Fatal("chain changed original", j.point.Source)
	}
}
func TestServiceMissingCoverageCreatesNewKeyAndNeverBlessesChangedWAL(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{true: "changed", false: "matching"}[changed], func(t *testing.T) {
			checkServiceMissingCoverageCreatesNewKeyAndNeverBlessesChangedWAL(t, changed)
		})
	}
}
func TestServiceNoReplayAfterUnknownOrTerminalOutcome(t *testing.T) {
	s, j, h, task, in := serviceFixture()
	h.fail = "barrier"
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || j.status.State != RecoveryRequired {
		t.Fatal(err, j.status)
	}
	count := len(h.calls)
	h.fail = ""
	_, err = s.Prepare(t.Context(), task, in)
	if !errors.Is(err, ErrRecovery) || len(h.calls) != count {
		t.Fatal("replayed", err, h.calls)
	}
}
func TestServiceAtomicCompleteLostReplyAndClose(t *testing.T) {
	for _, failure := range []string{"complete_before", "complete_after", "close"} {
		t.Run(failure, func(t *testing.T) {
			checkServiceAtomicCompleteLostReplyAndClose(t, failure)
		})
	}
}
func TestServiceFaultsRetainClaimAndDisposeGuards(t *testing.T) {
	for _, where := range []string{"stop_writers", "barrier", "verify", "stop_replica", "hold", "recheck", "schema"} {
		t.Run(where, func(t *testing.T) {
			checkHostFault(t, where)
		})
	}
	for _, stage := range []Stage{WritersStopIntent, WritersStopped, CoverageUploaded, Verified, ReplicaStopIntent, ReplicaStopped} {
		t.Run(string(stage), func(t *testing.T) {
			checkJournalFault(t, stage)
		})
	}
}
func TestServiceContradictionCannotChangeClaimedMode(t *testing.T) {
	s, j, h, task, in := supersedeFixture()
	h.fail = "contradict_replica"
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || j.status.State != RecoveryRequired || j.history.Intent.Mode != SupersedeReverify || slices.Contains(h.calls, "capture") || slices.Contains(h.calls, "barrier") {
		t.Fatal(err, j.status, h.calls)
	}
}
func TestServiceRequiredCredentialsExpireBeforeClaim(t *testing.T) {
	s, j, h, task, in := supersedeFixture()
	j.facts.CredentialExpiresAt = testNow
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || slices.Contains(j.calls, "claim") || slices.Contains(h.calls, "verify") || j.status.State != Failed {
		t.Fatal(err, j.status, h.calls, j.calls)
	}
}
func TestServiceAbandonUnknownOldHeadAndExpiredCredentials(t *testing.T) {
	s, j, h, task, in := supersedeFixture()
	in = Input{Kind: AbandonTask, PreparationID: j.facts.Lineage.Previous.Input.PreparationID, App: in.App, DatabaseID: in.DatabaseID}
	task.Kind = AbandonTask
	task.InputSHA256 = mustHash(in)
	j.facts.CredentialExpiresAt = testNow
	oldHistory, _ := fixturePrepared(Fresh, Snapshot)
	for _, e := range oldHistory.Events {
		if e.Snapshot != nil {
			j.facts.UncertainSnapshot = e.Snapshot
		}
	}
	h.oldErr = errFault
	r, err := s.Abandon(t.Context(), task, in)
	if err != nil || r.Receipt.Outcome != AbandonedOutcome || j.abandonObserved != ObjectUnknown || r.Receipt.OldObject != ObjectUnknown || !j.observedHeld || r.Receipt.Fence.OperationID != j.facts.Fence.OperationID || h.held {
		t.Fatal(r, err, j.status, h.calls)
	}
	for _, effect := range []string{"barrier", "upload", "capture", "verify", "stop_replica", "stop_writers"} {
		if slices.Contains(h.calls, effect) {
			t.Fatal(effect, h.calls)
		}
	}
}
func TestHostPortsContainNoStartRestartOrExec(t *testing.T) {
	typ := reflect.TypeFor[HostGuard]()
	for n := 0; n < typ.NumMethod(); n++ {
		name := typ.Method(n).Name
		if name == "Start" || name == "Restart" || name == "Exec" {
			t.Fatal(name)
		}
	}
}

func TestServiceCrossedSnapshotRefusesBeforeUpload(t *testing.T) {
	s, j, h, task, in := serviceFixture()
	h.obs.Replica = Stopped
	h.crossSnapshot = true
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || slices.Contains(h.calls, "upload") || j.status.State != RecoveryRequired {
		t.Fatalf("crossed snapshot reached upload: err=%v host=%v", err, h.calls)
	}
}
func TestServiceCapturedSnapshotAndReverifyFaults(t *testing.T) {
	for _, where := range []string{"capture", "upload", "verify", "recheck", "schema"} {
		t.Run(where, func(t *testing.T) {
			checkServiceCapturedSnapshotAndReverifyFaults(t, where)
		})
	}
}
func TestServiceKnownCompletionMustMatchRealTask(t *testing.T) {
	s, j, h, task, in := serviceFixture()
	r, err := s.Prepare(t.Context(), task, in)
	if err != nil {
		t.Fatal(err)
	}
	r.Receipt.Task.OperationID = "foreign"
	j.status.Receipt = &r.Receipt
	count := len(h.calls)
	if _, err := s.Prepare(t.Context(), task, in); err == nil || len(h.calls) != count {
		t.Fatal("crossed task or replay", err, h.calls)
	}
}
func TestServiceBothUnknownUploadResponsesNeverReplay(t *testing.T) {
	for _, response := range []string{"first_create", "second_capability_check"} {
		t.Run(response, func(t *testing.T) {
			checkServiceBothUnknownUploadResponsesNeverReplay(t, response)
		})
	}
}
func countCall(calls []string, want string) int {
	n := 0
	for _, call := range calls {
		if call == want {
			n++
		}
	}
	return n
}

func TestServiceAbandonExpiredPointAtomicCompletion(t *testing.T) {
	for _, failure := range []string{"complete_before", "complete_after", "close"} {
		t.Run(failure, func(t *testing.T) {
			checkServiceAbandonExpiredPointAtomicCompletion(t, failure)
		})
	}
}
func TestServiceClaimFaultClassification(t *testing.T) {
	for _, where := range []string{"claim_before", "claim_after"} {
		t.Run(where, func(t *testing.T) {
			checkServiceClaimFaultClassification(t, where)
		})
	}
}

func TestServicePreparationBudgetDoesNotSpendPointLifetime(t *testing.T) {
	s, j, h, task, in := serviceFixture()
	h.delayVerify = 15 * time.Minute
	result, err := s.Prepare(t.Context(), task, in)
	if err != nil || result.Receipt.Outcome != PreparedOutcome {
		t.Fatal("preparation budget consumed the point lifetime", err)
	}
	if !j.point.ExpiresAt.Equal(j.point.PreparedAt.Add(PointLifetime)) {
		t.Fatal("point expiry was tied to claim rather than completion", j.point.ExpiresAt, j.point.PreparedAt)
	}
}

func checkServiceFreshLTXAndStoppedSnapshot(t *testing.T, running bool) {
	t.Helper()
	s, j, h, task, in := serviceFixture()
	if !running {
		h.obs.Replica = Stopped
	}
	result, err := s.Prepare(t.Context(), task, in)
	if err != nil || result.Receipt.Validate() != nil {
		t.Fatalf("result %+v, %v; host %v journal %v", result, err, h.calls, j.calls)
	}
	if !j.observedHeld || h.held || !h.closed {
		t.Fatalf("guard lifetime: held=%v observed=%v closed=%v", h.held, j.observedHeld, h.closed)
	}
	if running && (!slices.Contains(h.calls, "barrier") || !slices.Contains(h.calls, "stop_replica")) {
		t.Fatal(h.calls)
	}
	if !running && (!slices.Contains(h.calls, "capture") || !slices.Contains(h.calls, "upload") || slices.Contains(h.calls, "stop_replica")) {
		t.Fatal(h.calls)
	}
}

func checkServiceMissingCoverageCreatesNewKeyAndNeverBlessesChangedWAL(t *testing.T, changed bool) {
	t.Helper()
	s, j, h, task, in := supersedeFixture()
	j.facts.Lineage.Coverage = nil
	j.facts.Lineage.OwnUploaded = false
	if changed {
		h.bundle.WAL.Inode++
	}
	_, err := s.Prepare(t.Context(), task, in)
	if changed {
		if err == nil || slices.Contains(j.calls, "claim") || slices.Contains(h.calls, "capture") {
			t.Fatalf("changed source claimed/captured: %v %v", err, h.calls)
		}
		return
	}
	if err != nil {
		t.Fatalf("%v host %v journal %v", err, h.calls, j.calls)
	}
	if j.point.Source.Origin != in.PreparationID || j.point.Source.Point.Snapshot.ObjectKey != SnapshotKey(j.facts.Scope, in.PreparationID) || !slices.Contains(h.calls, "upload") {
		t.Fatal(j.point)
	}
}

func checkServiceAtomicCompleteLostReplyAndClose(t *testing.T, failure string) {
	t.Helper()
	s, j, h, task, in := serviceFixture()
	j.fail = failure
	h.closeErr = failure == "close"
	r, err := s.Prepare(t.Context(), task, in)
	if j.completeCalls != 1 {
		t.Fatal("completion repeated", j.completeCalls)
	}
	if failure == "complete_before" {
		if !errors.Is(err, ErrRecovery) || j.status.State != RecoveryRequired {
			t.Fatal(err, j.status)
		}
		return
	}
	if err != nil || r.Receipt.Outcome != PreparedOutcome || j.status.State != Succeeded {
		t.Fatal(r, err, j.status)
	}
	if failure == "close" && r.CleanupError == nil {
		t.Fatal("lost cleanup diagnostic")
	}
	calls := len(h.calls)
	again, err := s.Prepare(t.Context(), task, in)
	if err != nil || again.Receipt != r.Receipt || len(h.calls) != calls {
		t.Fatal("known success replayed", err)
	}
}

func checkServiceCapturedSnapshotAndReverifyFaults(t *testing.T, where string) {
	t.Helper()
	s, j, h, task, in := supersedeFixture()
	j.facts.Lineage.Coverage = nil
	j.facts.Lineage.OwnUploaded = false
	h.fail = where
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil {
		t.Fatal("expected failure", h.calls)
	}
	if slices.Contains(j.calls, "claim") && j.status.State != RecoveryRequired {
		t.Fatal(j.status)
	}
	if h.held || !h.closed {
		t.Fatal(h.calls)
	}
	if slices.Contains(h.calls, "barrier") {
		t.Fatal(h.calls)
	}
}

func checkServiceBothUnknownUploadResponsesNeverReplay(t *testing.T, response string) {
	t.Helper()
	s, j, h, task, in := supersedeFixture()
	j.facts.Lineage.Coverage = nil
	j.facts.Lineage.OwnUploaded = false
	h.fail = "upload"
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || j.status.State != RecoveryRequired {
		t.Fatal(err, j.status)
	}
	if count := len(h.calls); count == 0 {
		t.Fatal(h.calls)
	}
	if n := countCall(h.calls, "upload"); n != 1 {
		t.Fatal("upload invocation count", n)
	}
	h.fail = ""
	_, err = s.Prepare(t.Context(), task, in)
	if !errors.Is(err, ErrRecovery) || countCall(h.calls, "upload") != 1 {
		t.Fatal("unknown upload replayed", err, h.calls)
	}
}

func checkServiceAbandonExpiredPointAtomicCompletion(t *testing.T, failure string) {
	t.Helper()
	s, j, h, task, in := serviceFixture()
	history, point := fixturePrepared(Fresh, LTX)
	h.clock = testNow.Add(11 * time.Minute)
	h.obs = fixtureObservation()
	j.facts.CredentialExpiresAt = testNow
	j.facts.Lineage = &Lineage{Previous: history.Intent, Terminal: true, Unconsumed: true, OwnStopped: true, OwnUploaded: true, Baseline: &StoppedRecord{PreparationID: point.PreparationID, Scope: history.Intent.Scope, Fence: history.Intent.Fence, Bundle: point.Stopped}, Coverage: &CoverageRecord{Source: point.Source, Scope: history.Intent.Scope, Fence: history.Intent.Fence, BaselinePreparationID: point.PreparationID, BaselineSHA256: mustHash(point.Stopped)}}
	j.facts.Point = &point
	in.Kind = AbandonTask
	task.Kind = AbandonTask
	task.InputSHA256 = mustHash(in)
	j.fail = failure
	h.closeErr = failure == "close"
	r, err := s.Abandon(t.Context(), task, in)
	if failure == "complete_before" {
		if !errors.Is(err, ErrRecovery) || j.status.State != RecoveryRequired {
			t.Fatal(err, j.status)
		}
		return
	}
	if err != nil || r.Receipt.Outcome != AbandonedOutcome || r.Receipt.PreparedSHA256 != mustHash(point) || !j.observedHeld {
		t.Fatal(r, err, j.status)
	}
	if failure == "close" && r.CleanupError == nil {
		t.Fatal("cleanup diagnostic missing")
	}
	count := len(h.calls)
	again, err := s.Abandon(t.Context(), task, in)
	if err != nil || again.Receipt != r.Receipt || len(h.calls) != count {
		t.Fatal("abandon replayed", err)
	}
}

func checkServiceClaimFaultClassification(t *testing.T, where string) {
	t.Helper()
	s, j, h, task, in := serviceFixture()
	j.fail = where
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil {
		t.Fatal("unknown claim reported success")
	}
	want := Failed
	if where == "claim_after" {
		want = RecoveryRequired
	}
	if j.status.State != want || slices.Contains(h.calls, "stop_writers") {
		t.Fatal(j.status, h.calls)
	}
	calls := len(h.calls)
	j.fail = ""
	if _, err := s.Prepare(t.Context(), task, in); err == nil || len(h.calls) != calls {
		t.Fatal("replayed claim", h.calls)
	}
}

func checkHostFault(t *testing.T, where string) {
	t.Helper()
	s, j, h, task, in := serviceFixture()
	h.fail = where
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || j.status.State != RecoveryRequired || !h.closed || h.held {
		t.Fatalf("%s error %v state %v calls %v", where, err, j.status, h.calls)
	}
}

func checkJournalFault(t *testing.T, stage Stage) {
	t.Helper()
	s, j, h, task, in := serviceFixture()
	j.fail = string(stage)
	_, err := s.Prepare(t.Context(), task, in)
	if err == nil || j.status.State != RecoveryRequired || !h.closed || h.held {
		t.Fatal(err, j.status, h.calls)
	}
}

func TestServiceSupersedeLiveNeverStartsReplica(t *testing.T) {
	s, j, h, task, in := supersedeFixture()
	j.facts.Lineage.Baseline = nil
	j.facts.Lineage.Coverage = nil
	j.facts.Lineage.OwnStopped = false
	j.facts.Lineage.OwnUploaded = false
	h.obs.Replica = Running
	h.obs.WritersStopped = false
	r, err := s.Prepare(t.Context(), task, in)
	if err != nil || j.history.Intent.Mode != SupersedeLive || r.Receipt.Fence.OperationID != j.facts.Fence.OperationID {
		t.Fatal(r, err, h.calls)
	}
	if j.point.Source.Origin != in.PreparationID || !slices.Contains(h.calls, "barrier") || !slices.Contains(h.calls, "stop_replica") || slices.Contains(h.calls, "capture") || slices.Contains(h.calls, "upload") {
		t.Fatal(j.point, h.calls)
	}
}
func TestServiceAllocatedEmptyKeepsAbsenceAndAllocationProof(t *testing.T) {
	s, j, h, task, in := serviceFixture()
	scope := j.facts.Scope
	scope.ReplicaCommitted = false
	scope.ActivatedCredentialVersion = 0
	scope.Schema.State = data.AllocatedEmpty
	scope.Schema.Marker = data.EmptyMarker
	scope.Schema.CatalogSHA256 = data.EmptyCatalogSHA256
	scope.EmptyAllocation = &EmptyAllocation{AllocationSHA256: hash("1"), NeverWrittenSHA256: hash("2"), DatabaseID: scope.DatabaseID, IncarnationID: scope.IncarnationID, Location: j.facts.Location}
	j.facts.Scope = scope
	h.obs = Observation{Replica: Missing, WritersStopped: true, JobsAbsent: true, Owned: true, Schema: scope.Schema}
	h.bundle.DB = Member{Presence: Absent}
	h.bundle.WAL = Member{Presence: Absent}
	_, err := s.Prepare(t.Context(), task, in)
	if err != nil || j.point.Stopped.DB.Presence != Absent || j.point.Source.Point.Schema.State != data.VerifiedEmpty || j.history.Intent.Arm != Empty {
		t.Fatal(err, j.point, h.calls)
	}
	if !reflect.DeepEqual(j.history.Intent.Scope.EmptyAllocation, scope.EmptyAllocation) || h.scope.ActivatedCredentialVersion != 0 {
		t.Fatal("allocation proof or activation changed")
	}
}
