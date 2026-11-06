package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

// Every successful response crosses the production wire decoder, not a typed
// shortcut. Requests omit only the random transport correlation ID in comparisons.
type lifecycleWireHost struct {
	t                 *testing.T
	action            tuiAction
	final             ops.State
	disconnect        bool
	failedReads       int
	requests          []string
	acceptanceFailure string
	applies           int
}

const lifecyclePlanID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func (h *lifecycleWireHost) Call(_ context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
	h.t.Helper()
	if req.Op != "status" {
		h.requests = append(h.requests, target.Name+" "+req.Op+" "+string(req.Args))
	}
	if req.Op == "operation" {
		return h.operation(req)
	}
	if req.Op == "apply" {
		h.applies++
		if h.applies == 1 && h.acceptanceFailure != "" {
			return h.failAcceptance(req)
		}
	}
	value := h.response(req)
	raw, err := json.Marshal(result.Success("brine host "+req.Op, value))
	if err != nil {
		h.t.Fatal(err)
	}
	return dispatch.DecodeResponse(raw, req.Op)
}

func (h *lifecycleWireHost) response(req dispatch.Request) any {
	switch req.Op {
	case "status":
		return fixtureReport()
	case "apply":
		return jobs.Accepted{Status: "accepted", OperationID: "op-u4"}
	case "lifecycle":
		action := map[tuiAction]plan.ChangeKind{actionRestart: plan.RestartApp, actionStop: plan.StopApp, actionStart: plan.StartApp}[h.action]
		return apps.ConfigPlan{PlanID: lifecyclePlanID, Kind: plan.Update, Lifecycle: action, Conflicts: []plan.Diagnostic{}}
	case "rollback":
		return apps.RollbackPlan{PlanID: lifecyclePlanID, Kind: plan.Update, ReleaseID: "rel-3c02aa10", Compatibility: "stateless_compatible", Conflicts: []plan.Diagnostic{}}
	default:
		h.t.Fatalf("unallowed request %+v", req)
		return nil
	}
}

func (h *lifecycleWireHost) failAcceptance(req dispatch.Request) (result.Envelope, error) {
	switch h.acceptanceFailure {
	case "disconnect":
		return result.Envelope{}, result.New(result.TransportFailure, nil)
	case "malformed":
		return dispatch.DecodeResponse([]byte(`{"schema_version":1,"command":"brine host apply","ok":true,"data":{"status":"accepted","operation_id":"invalid id"},"error":null}`), req.Op)
	default:
		raw, err := json.Marshal(result.Failure("brine host apply", result.New(result.InternalError, nil)))
		if err != nil {
			h.t.Fatal(err)
		}
		return dispatch.DecodeResponse(raw, req.Op)
	}
}

func (h *lifecycleWireHost) operation(req dispatch.Request) (result.Envelope, error) {
	var args dispatch.OperationArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		h.t.Fatal(err)
	}
	if args.AfterCursor == 1 && h.disconnect && h.failedReads < 3 {
		h.failedReads++
		return result.Envelope{}, result.New(result.TransportFailure, nil)
	}
	status := operationFixture(ops.Starting, args.AfterCursor, 1)
	status.Operation.ID, status.Operation.PlanID = "op-u4", lifecyclePlanID
	if args.AfterCursor > 0 {
		status.Operation.State = h.final
		status.Events[0].State = h.final
		if h.final != ops.Succeeded {
			failure := ops.Event{Sequence: status.NextCursor + 1, Kind: "failure", Payload: json.RawMessage(`{"code":"executor_failed"}`), CreatedAt: fixtureReadTime}
			status.Events = append(status.Events, failure)
			status.NextCursor++
		}
	}
	raw, err := json.Marshal(result.Success("brine host operation", status))
	if err != nil {
		h.t.Fatal(err)
	}
	return dispatch.DecodeResponse(raw, req.Op)
}

func wireDependencies(t *testing.T, h OperationClient) Dependencies {
	t.Helper()
	deps := testDependencies(t, &bytes.Buffer{}, &bytes.Buffer{})
	deps.OperationClient = h
	deps.LoadOperationTarget = func(dir, name string) (transport.Target, error) {
		if dir != "/fixture config's targets" || name != "fixture" {
			t.Fatalf("wrong selection %q %q", dir, name)
		}
		return transport.Target{Name: name}, nil
	}
	return deps
}

var lifecycleSelection = operationFlags{target: "fixture", configDir: "/fixture config's targets"}

func machineJSON(t *testing.T, deps Dependencies, args ...string) (json.RawMessage, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	deps.Stdout, deps.Stderr = &out, &diagnostics
	args = append(args, "--json", "--target", lifecycleSelection.target, "--config-dir", lifecycleSelection.configDir)
	err := Execute(deps, args)
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("JSON output: %v: %s", decodeErr, out.String())
	}
	return envelope.Data, err
}

type lifecycleJourney struct {
	Plan      json.RawMessage
	Accepted  jobs.Accepted
	Events    []ops.Event
	Operation ops.Operation
	Outcome   *ops.TaskOutcome
	Guidance  string
}

func machineJourney(t *testing.T, h *lifecycleWireHost) lifecycleJourney {
	t.Helper()
	deps := wireDependencies(t, h)
	p, err := machineJSON(t, deps, string(h.action), "fixture-api")
	if err != nil {
		t.Fatal(err)
	}
	a, err := machineJSON(t, deps, "apply", lifecyclePlanID, "--idempotency-key", "u4-fixed-key")
	if err != nil {
		t.Fatal(err)
	}
	var accepted jobs.Accepted
	if err := json.Unmarshal(a, &accepted); err != nil {
		t.Fatal(err)
	}
	journey := lifecycleJourney{Plan: p, Accepted: accepted}
	var cursor uint64
	for range 8 {
		raw, err := machineJSON(t, deps, "status", "--operation", accepted.OperationID, "--after-cursor", fmt.Sprint(cursor))
		if err != nil && result.Classify(err).Code() != result.TransportFailure {
			t.Fatal(err)
		}
		if err != nil {
			continue
		}
		var status jobs.Status
		if err := json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
		journey.Events = append(journey.Events, status.Events...)
		journey.Operation, journey.Outcome, cursor = status.Operation, status.Outcome, status.NextCursor
		if status.Operation.State.IsTerminal() {
			break
		}
	}
	journey.Guidance = recoveryGuidance(lifecycleSelection, journey.Operation, journey.Events, true)
	return journey
}

func tuiJourney(t *testing.T, h *lifecycleWireHost) lifecycleJourney {
	t.Helper()
	m := start(newTUIModel(t.Context(), wireDependencies(t, h), lifecycleSelection))
	m.after = func(_ time.Duration, msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }
	m, _ = press(m, "enter", "p")
	m.actionIndex = slices.Index(m.actions(), h.action)
	m, _ = press(m, "enter")
	if m.review.State != loadReady {
		t.Fatalf("review failed: %+v", m.review)
	}
	var p any = m.review.Value.Config
	if h.action == actionRollback {
		p = m.review.Value.Rollback
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	m.newKey = func() (string, error) { return "u4-fixed-key", nil }
	m, cmd := issueForTest(m)
	m, cmd = updateTUI(m, cmd())
	accepted := *m.issued.Accepted
	m, _ = step(m, cmd())
	if h.disconnect {
		if m.observe.Phase != observePaused || m.observe.Cursor != 1 {
			t.Fatalf("disconnect not paused at cursor 1: %+v", m.observe)
		}
		m, _ = press(m, "r")
	}
	if m.observe.Phase != observeSettled || m.issued != nil {
		t.Fatalf("not settled: %+v", m.observe)
	}
	o := m.observe
	return lifecycleJourney{Plan: raw, Accepted: accepted, Events: o.Events, Operation: o.Operation, Outcome: o.Outcome, Guidance: recoveryGuidance(o.Flags, o.Operation, o.Events, true)}
}

func TestTUILifecycleJSONParity(t *testing.T) {
	for _, action := range []tuiAction{actionRestart, actionStop, actionStart, actionRollback} {
		t.Run(string(action), func(t *testing.T) { testLifecycleOutcomes(t, action) })
	}
}

func testLifecycleOutcomes(t *testing.T, action tuiAction) {
	t.Helper()
	for _, state := range []ops.State{ops.Succeeded, ops.Failed, ops.RecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			compareLifecycleJourneys(t, action, state, false)
			compareLifecycleJourneys(t, action, state, true)
		})
	}
}

func compareLifecycleJourneys(t *testing.T, action tuiAction, state ops.State, disconnect bool) {
	t.Helper()
	machine := &lifecycleWireHost{t: t, action: action, final: state, disconnect: disconnect}
	terminal := &lifecycleWireHost{t: t, action: action, final: state, disconnect: disconnect}
	want, got := machineJourney(t, machine), tuiJourney(t, terminal)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("journeys differ\nmachine %+v\nTUI %+v", want, got)
	}
	if !slices.Equal(machine.requests, terminal.requests) {
		t.Fatalf("requests differ\nmachine %v\nTUI %v", machine.requests, terminal.requests)
	}
	if got.Operation.State != state || machine.applies != 1 || terminal.applies != 1 {
		t.Fatal("wrong verdict or repeated apply")
	}
	if state == ops.RecoveryRequired && !strings.Contains(got.Guidance, "Inspect") {
		t.Fatal("missing recovery guidance")
	}
}

func TestTUILifecycleWireErrorsPreserveOriginalRecovery(t *testing.T) {
	for _, failure := range []string{"disconnect", "malformed", "post-record host error"} {
		t.Run(failure, func(t *testing.T) { testWireRecovery(t, failure) })
	}
}

func testWireRecovery(t *testing.T, failure string) {
	t.Helper()
	machine := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded, acceptanceFailure: failure}
	deps := wireDependencies(t, machine)
	if _, err := machineJSON(t, deps, "restart", "fixture-api"); err != nil {
		t.Fatal(err)
	}
	_, firstErr := machineJSON(t, deps, "apply", lifecyclePlanID, "--idempotency-key", "u4-fixed-key")
	if firstErr == nil {
		t.Fatal("machine acceptance must be uncertain")
	}
	raw, err := machineJSON(t, deps, "apply", lifecyclePlanID, "--idempotency-key", "u4-fixed-key")
	if err != nil {
		t.Fatal(err)
	}
	var accepted jobs.Accepted
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded, acceptanceFailure: failure}
	m := start(newTUIModel(t.Context(), wireDependencies(t, h), lifecycleSelection))
	m, _ = press(m, "enter", "p", "enter")
	m.newKey = func() (string, error) { return "u4-fixed-key", nil }
	m, cmd := issueForTest(m)
	m, next := updateTUI(m, cmd())
	if next != nil || m.issued == nil || m.issued.Err == nil || m.issued.Err.Code() != result.Classify(firstErr).Code() {
		t.Fatal("uncertain acceptance lost or retried")
	}
	original := m.issued
	m, _ = updateTUI(m, keyMsg("esc"))
	m.flags = operationFlags{target: "elsewhere"}
	m, _ = updateTUI(m, keyMsg("u"))
	m, cmd = issueForTest(m)
	if cmd == nil {
		t.Fatal("explicit recovery disabled")
	}
	m, _ = updateTUI(m, cmd())
	if m.issued != original || m.issued.Accepted == nil || *m.issued.Accepted != accepted || h.applies != 2 || h.requests[1] != h.requests[2] || !slices.Equal(h.requests, machine.requests) {
		t.Fatalf("recovery changed identity: %v", h.requests)
	}
}
