package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestTUIDeploymentEngineParity(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) { deploymentEngineInitiators(t, failed) })
	}
}

func deploymentEngineInitiators(t *testing.T, failed bool) {
	for _, first := range []string{"json", "tui"} {
		t.Run(first+" initiates", func(t *testing.T) { testDeploymentEngineParity(t, failed, first) })
	}
}

func testDeploymentEngineParity(t *testing.T, failed bool, first string) {
	t.Helper()
	f := newDeploymentEngineFixture(t, failed, false)
	envelope, err := f.machine("plan", f.path)
	planned := deploymentEngineData[dispatch.Planned](t, envelope, err)
	s := startDeploymentEngineSession(t, f, tuiEntry{Spec: f.path}, "a3-parity", f)
	s.wait(t, "a apply")
	if f.requestCount("plan") != 2 || f.requestCount("apply") != 0 {
		t.Fatal("both real reviews must be ready before acceptance")
	}
	f.assertReleaseIntent(planned)
	var accepted jobs.Accepted
	if first == "json" {
		envelope, err = f.machine("apply", planned.PlanID, "--idempotency-key", "a3-parity")
		accepted = deploymentEngineData[jobs.Accepted](t, envelope, err)
	}
	s.approve(t)
	if first == "tui" {
		envelope, err = f.machine("apply", planned.PlanID, "--idempotency-key", "a3-parity")
		accepted = deploymentEngineData[jobs.Accepted](t, envelope, err)
	}
	if accepted.OperationID != f.operationID() {
		t.Fatal("same plan/key returned a different real operation")
	}
	f.assertMutations(2, 2)
	queued, err := f.state.GetOperation(f.ctx, accepted.OperationID)
	if err != nil || queued.State != ops.Queued || len(f.effects) != 0 {
		t.Fatal("executor gate opened before frozen reviews/recovered acceptance", queued, err)
	}
	close(f.gate)
	f.finish()
	status := f.assertOutcome()
	s.wait(t, "settled")
	var final tuiModel
	if failed {
		final = s.diagnoseExit(t, f)
		f.assertDiagnosis(final)
	} else {
		final = s.end(t, "quit")
	}
	assertDeploymentEngineObservation(t, final, status)
	if final.review.Value.Deployment == nil || !deploymentEnginePublicEqual(*final.review.Value.Deployment, planned) {
		t.Fatal("TUI and JSON did not retain the same actual release plan")
	}
	f.assertMutations(2, 2)
}

func (f *deploymentEngineFixture) assertReleaseIntent(planned dispatch.Planned) {
	f.t.Helper()
	p, desired, err := f.state.LoadPlan(f.ctx, planned.PlanID)
	if err != nil || p.Lifecycle != "" || p.Kind != plan.Create || p.App != "hello" || !desired.Stateless() || p.Image.Digest != f.image.Digest {
		f.t.Fatalf("not a genuine stateless release plan: %+v %v", p, err)
	}
}

func (f *deploymentEngineFixture) operationID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.launches) != 1 {
		f.t.Fatalf("launch count %d", len(f.launches))
	}
	return f.launches[0]
}

func (f *deploymentEngineFixture) requestCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, request := range f.requests {
		if request.Op == op {
			count++
		}
	}
	return count
}

func (f *deploymentEngineFixture) assertMutations(plans, applies int) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	planned, applied := 0, 0
	for _, request := range f.requests {
		switch request.Op {
		case "plan":
			planned++
		case "apply":
			applied++
		}
	}
	if len(f.launches) != 1 || planned != plans || applied != applies {
		f.t.Fatalf("launches=%v plans=%d applies=%d, want one launch, %d plans and %d applies", f.launches, planned, applied, plans, applies)
	}
}

func (f *deploymentEngineFixture) assertOutcome() jobs.Status {
	f.t.Helper()
	envelope, err := f.machine("status", "--operation", f.operationID())
	status := deploymentEngineData[jobs.Status](f.t, envelope, err)
	stored, err := f.state.GetOperation(f.ctx, status.Operation.ID)
	if err != nil || !deploymentEnginePublicEqual(stored, status.Operation) || stored.Kind != ops.Deploy {
		f.t.Fatal("status not backed by actual operation", stored, err)
	}
	events, err := f.state.EventsAfter(f.ctx, stored.ID, 0, jobs.EventPageLimit)
	if err != nil || !deploymentEnginePublicEqual(events, status.Events) || status.NextCursor == 0 {
		f.t.Fatalf("JSON did not read actual journal: %v\nactual=%s\nwire=%s", err, mustDeploymentEngineJSON(events), mustDeploymentEngineJSON(status.Events))
	}
	f.assertReleaseIntent(dispatch.Planned{PlanID: stored.PlanID})
	f.assertOrderedEvents(status)
	if f.pullFail {
		f.assertFailedRelease(status)
	} else {
		f.assertCommittedRelease(status)
	}
	return status
}

func (f *deploymentEngineFixture) assertCommittedRelease(status jobs.Status) {
	f.t.Helper()
	release, err := f.state.CurrentRelease(f.ctx, "hello")
	if err != nil || status.Operation.State != ops.Succeeded || release.ID != status.Operation.ID || release.PlanID != status.Operation.PlanID || len(release.Units) != 1 || release.Units[0].Hash != f.unitHash || release.CaddyGeneration != 1 || release.CaddyFile.Hash != f.routeHash || release.HostPort != 20000 || !deploymentEnginePublicEqual(release.Image, f.image) {
		f.t.Fatalf("no real committed release: %+v %+v %v", status.Operation, release, err)
	}
	if _, err := f.state.PreviousRelease(f.ctx, "hello"); !errors.Is(err, store.ErrNotFound) {
		f.t.Fatal("first release produced an unexpected previous head", err)
	}
	generation, err := f.state.Generation(f.ctx)
	if err != nil || generation != 1 {
		f.t.Fatal("commit did not advance generation once", generation, err)
	}
	want := []string{"pull", "verify_image", "stage_unit", "install_unit", "reload_units", "start_unit", "health_routed=false", "publish_route", "health_routed=true"}
	if !slices.Equal(f.effects, want) {
		f.t.Fatalf("real executor effects %v, want %v", f.effects, want)
	}
}

func (f *deploymentEngineFixture) assertFailedRelease(status jobs.Status) {
	f.t.Helper()
	if status.Operation.State != ops.Failed || !slices.Equal(f.effects, []string{"pull"}) || f.unitHash != "" || f.active {
		f.t.Fatalf("definite pull failure leaked effects: %+v %v", status.Operation, f.effects)
	}
	if _, err := f.state.CurrentRelease(f.ctx, "hello"); !errors.Is(err, store.ErrNotFound) {
		f.t.Fatal("failed deployment has a current release", err)
	}
	generation, err := f.state.Generation(f.ctx)
	if err != nil || generation != 0 {
		f.t.Fatal("failed deployment advanced generation", generation, err)
	}
	envelope, err := f.machine("status")
	report := deploymentEngineData[struct {
		Apps []json.RawMessage `json:"apps"`
	}](f.t, envelope, err)
	if len(report.Apps) != 0 {
		f.t.Fatal("failed first release fabricated committed app status")
	}
}

func (f *deploymentEngineFixture) assertOrderedEvents(status jobs.Status) {
	f.t.Helper()
	completed := []string{}
	failedPull, failure := false, false
	for i, event := range status.Events {
		if event.Sequence != uint64(i+1) {
			f.t.Fatal("journal skipped or reordered events", status.Events)
		}
		if event.Kind == "state" {
			continue
		}
		var payload ops.StepPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			f.t.Fatal(err)
		}
		if event.Kind == "step" && payload.Outcome == "completed" {
			completed = append(completed, payload.Step)
		}
		failedPull = failedPull || event.Kind == "step" && payload.Step == "pull_image" && payload.Outcome == "failed" && payload.Code == "digest_mismatch"
		failure = failure || event.Kind == "failure" && payload.Code == "executor_failed"
	}
	if f.pullFail {
		if !failedPull || !failure || !slices.Equal(completed, []string{"preflight"}) {
			f.t.Fatal("missing actual pull failure journal", status.Events)
		}
		return
	}
	want := []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "check_direct", "publish_route", "check_routed", "commit"}
	if failure || !slices.Equal(completed, want) {
		f.t.Fatalf("real success journal completed %v, want %v", completed, want)
	}
}

func assertDeploymentEngineObservation(t *testing.T, m tuiModel, status jobs.Status) {
	t.Helper()
	if m.observe == nil || m.observe.Phase != observeSettled || m.observe.Cursor != status.NextCursor || !reflect.DeepEqual(m.observe.Operation, status.Operation) || !deploymentEnginePublicEqual(m.observe.Events, status.Events) {
		t.Fatalf("TUI differs from same-store JSON journal: %+v; JSON %+v", m.observe, status)
	}
}

func (f *deploymentEngineFixture) assertDiagnosis(m tuiModel) {
	f.t.Helper()
	envelope, err := f.machine("diagnose", "hello")
	report := deploymentEngineData[diagnose.Report](f.t, envelope, err)
	if m.diagnosis.State != loadReady || !reflect.DeepEqual(report, m.diagnosis.Value) || len(report.Apps) != 1 || report.Apps[0].CurrentRelease.Status != "absent" {
		f.t.Fatal("failed-uncommitted-app diagnosis differs between real readers")
	}
	if !slices.ContainsFunc(report.Findings, func(finding diagnose.Finding) bool {
		return finding.Code == "operation_failed" && len(finding.NextOperations) != 0
	}) {
		f.t.Fatal("actual failed operation has no actionable diagnosis", report.Findings)
	}
}

type deploymentEngineCursorFault struct {
	fixture *deploymentEngineFixture
	mu      sync.Mutex
	fail    bool
	cursors []uint64
}

func (h *deploymentEngineCursorFault) Call(ctx context.Context, selected transport.Target, req dispatch.Request) (result.Envelope, error) {
	if req.Op != "operation" {
		return h.fixture.Call(ctx, selected, req)
	}
	var args dispatch.OperationArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return result.Envelope{}, err
	}
	h.mu.Lock()
	h.cursors = append(h.cursors, args.AfterCursor)
	fail := h.fail && args.AfterCursor > 0
	h.mu.Unlock()
	if fail {
		return result.Envelope{}, result.New(result.TransportFailure, nil)
	}
	return h.fixture.Call(ctx, selected, req)
}

func TestTUIDeploymentCursorDisconnect(t *testing.T) {
	f := newDeploymentEngineFixture(t, false, false)
	envelope, err := f.machine("plan", f.path)
	planned := deploymentEngineData[dispatch.Planned](t, envelope, err)
	envelope, err = f.machine("apply", planned.PlanID, "--idempotency-key", "a3-cursor")
	accepted := deploymentEngineData[jobs.Accepted](t, envelope, err)
	envelope, err = f.machine("status", "--operation", accepted.OperationID)
	initial := deploymentEngineData[jobs.Status](t, envelope, err)
	if initial.NextCursor == 0 || initial.Operation.State != ops.Queued {
		t.Fatal("cursor case requires a real nonzero queued journal", initial)
	}
	fault := &deploymentEngineCursorFault{fixture: f, fail: true}
	s := startDeploymentEngineSession(t, f, tuiEntry{Operation: accepted.OperationID}, "", fault)
	s.wait(t, "Observation paused after 3 failed reads")
	fault.assertPaused(t, initial.NextCursor)
	close(f.gate)
	f.finish()
	envelope, err = f.machine("status", "--operation", accepted.OperationID, "--after-cursor", strconv.FormatUint(initial.NextCursor, 10))
	tail := deploymentEngineData[jobs.Status](t, envelope, err)
	if len(tail.Events) == 0 || tail.Events[0].Sequence != initial.NextCursor+1 {
		t.Fatal("JSON continuation skipped the exact nonzero cursor", tail)
	}
	fault.mu.Lock()
	fault.fail = false
	fault.mu.Unlock()
	writeKeys(t, s.keys, "r")
	s.wait(t, "settled")
	final := s.end(t, "quit")
	whole := f.assertOutcome()
	if !reflect.DeepEqual(append(slices.Clone(initial.Events), tail.Events...), whole.Events) {
		t.Fatal("JSON cursor continuation reset, duplicated or lost actual events")
	}
	assertDeploymentEngineObservation(t, final, whole)
	fault.mu.Lock()
	defer fault.mu.Unlock()
	if len(fault.cursors) != 5 || fault.cursors[4] != initial.NextCursor {
		t.Fatal("explicit TUI retry changed cursor", fault.cursors)
	}
	f.assertMutations(1, 1)
}

func (h *deploymentEngineCursorFault) assertPaused(t *testing.T, cursor uint64) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.cursors, []uint64{0, cursor, cursor, cursor}) {
		t.Fatal("retry bound reset or skipped cursor", h.cursors)
	}
}

func deploymentEngineEnvelope(t *testing.T, raw []byte) result.Envelope {
	t.Helper()
	var envelope result.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("invalid command JSON %v: %s", err, raw)
	}
	return envelope
}

func deploymentEngineFailureReport(t *testing.T, envelope result.Envelope) deploymentReport {
	t.Helper()
	raw, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatal(err)
	}
	var report deploymentReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func (f *deploymentEngineFixture) assertReport(report deploymentReport, complete bool) {
	f.t.Helper()
	if report.Review.App != "hello" || len(report.Steps) != 2 || report.Steps[1].Name != "release" || report.Steps[1].OperationID != f.operationID() || report.Steps[1].Complete != complete || report.Steps[1].Key == "" {
		f.t.Fatalf("combined deploy lost real release checkpoint: %+v", report)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.ContainsFunc(f.requests, func(req dispatch.Request) bool {
		return req.Op == "apply" && bytes.Contains(req.Args, []byte(report.Steps[1].Key))
	}) {
		f.t.Fatal("checkpoint key differs from real accepted request")
	}
}

func deploymentEnginePublicEqual(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	var leftValue, rightValue any
	return leftErr == nil && rightErr == nil && json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func mustDeploymentEngineJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
