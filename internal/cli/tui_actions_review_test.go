package cli

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func reviewWire(t *testing.T, op string, value any) (result.Envelope, error) {
	t.Helper()
	raw, err := json.Marshal(result.Success("brine host "+op, value))
	if err != nil {
		t.Fatal(err)
	}
	return dispatch.DecodeResponse(raw, op)
}

func reviewConnected(t *testing.T, client OperationClient) tuiModel {
	t.Helper()
	m := start(newTUIModel(t.Context(), wireDependencies(t, client), lifecycleSelection, tuiEntry{}))
	m, _ = press(m, "enter", "p", "enter")
	m.newKey = func() (string, error) { return "review-original-key", nil }
	if m.review.State != loadReady {
		t.Fatalf("review failed: %+v", m.review)
	}
	return m
}

func TestReviewU4RejectCrossedLifecycleBeforeConfirmation(t *testing.T) {
	for _, action := range []plan.ChangeKind{plan.RemoveApp, plan.StopApp, ""} {
		t.Run(string(action), func(t *testing.T) { reviewCrossedLifecycle(t, action) })
	}
}

func reviewCrossedLifecycle(t *testing.T, actual plan.ChangeKind) {
	t.Helper()
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded}
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op == "lifecycle" {
			return reviewWire(t, req.Op, apps.ConfigPlan{PlanID: lifecyclePlanID, Kind: plan.Update, Lifecycle: actual, Conflicts: []plan.Diagnostic{}})
		}
		return h.Call(ctx, target, req)
	})
	m := start(newTUIModel(t.Context(), wireDependencies(t, client), lifecycleSelection, tuiEntry{}))
	m, _ = press(m, "enter", "p", "enter")
	keys := 0
	m.newKey = func() (string, error) { keys++; return "review-original-key", nil }
	m, cmd := issueForTest(m)
	if cmd == nil {
		if m.review.Err == nil || m.review.Err.Code() != result.TransportInvalidResponse || keys != 0 || h.applies != 0 {
			t.Fatal("crossed plan did not fail closed at the response boundary")
		}
		return
	}
	m, _ = updateTUI(m, cmd())
	t.Fatalf("restart selection accepted lifecycle=%q; applies=%d issued_action=%q plan=%s key=%s", actual, h.applies, m.issued.Review.Action, m.issued.Review.PlanID, m.issued.Review.Key)
}

func TestReviewU4WireTerminalIdentityControls(t *testing.T) {
	for _, bad := range []string{"app", "plan", "operation", "state", "payload", "full page", "valid"} {
		t.Run(bad, func(t *testing.T) { reviewTerminalIdentity(t, bad) })
	}
}

func reviewTerminalIdentity(t *testing.T, bad string) {
	t.Helper()
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded}
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "operation" {
			return h.Call(ctx, target, req)
		}
		status := operationFixture(ops.Succeeded, 0, 1)
		status.Operation.ID, status.Operation.PlanID = "op-u4", lifecyclePlanID
		switch bad {
		case "app":
			status.Operation.App = "different-app"
		case "plan":
			status.Operation.PlanID = fixtureDigest("b")
		case "operation":
			status.Operation.ID = "crossed-operation"
		case "state":
			status.Operation.State = "not-a-state"
		case "payload":
			status.Events[0] = ops.Event{Sequence: 1, Kind: "failure", Payload: json.RawMessage(`{"code":"executor_failed","password":"hunter2"}`), CreatedAt: fixtureReadTime}
		case "full page":
			status = operationFixture(ops.Succeeded, 0, jobs.EventPageLimit)
			status.Operation.ID, status.Operation.PlanID = "op-u4", lifecyclePlanID
		}
		return reviewWire(t, req.Op, status)
	})
	m, cmd := issueForTest(reviewConnected(t, client))
	original := m.issued
	m, cmd = updateTUI(m, cmd())
	m, next := updateTUI(m, cmd())
	if bad == "valid" {
		if m.issued != nil || m.observe.Phase != observeSettled {
			t.Fatal("exact terminal did not settle")
		}
		return
	}
	if m.issued != original || original.Review.Key != "review-original-key" {
		t.Fatal("unvalidated or undrained result lost original identity")
	}
	if bad == "full page" {
		if next == nil || !strings.Contains(m.exitNotice(), "not been fully observed") {
			t.Fatal("history drain/notice lost")
		}
		return
	}
	if next != nil || m.observe.Err == nil || m.observe.Err.Code() != result.TransportInvalidResponse {
		t.Fatalf("crossed response not paused: %+v", m.observe)
	}
	if strings.Contains(view(m), "hunter2") {
		t.Fatal("unvalidated payload leaked")
	}
}

func TestReviewU4WireConflictAndNoopCannotApply(t *testing.T) {
	for _, kind := range []plan.Kind{plan.Conflict, plan.NoOp} {
		t.Run(string(kind), func(t *testing.T) { reviewBlocked(t, kind) })
	}
}

func reviewBlocked(t *testing.T, kind plan.Kind) {
	t.Helper()
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded}
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "lifecycle" {
			return h.Call(ctx, target, req)
		}
		p := apps.ConfigPlan{PlanID: lifecyclePlanID, Kind: kind, Lifecycle: plan.RestartApp, Conflicts: []plan.Diagnostic{}}
		if kind == plan.Conflict {
			p.Conflicts = []plan.Diagnostic{{Code: plan.ArtifactDrift, Field: "unit"}}
		}
		return reviewWire(t, req.Op, p)
	})
	m := reviewConnected(t, client)
	m.newKey = func() (string, error) { t.Fatal("blocked plan minted key"); return "", nil }
	m, cmd := issueForTest(m)
	if cmd != nil || m.issued != nil || h.applies != 0 {
		t.Fatal("blocked plan issued")
	}
}

func TestReviewU4NarrowSameKeyRecovery(t *testing.T) {
	for _, width := range []int{1, 20, 30, 50, 80} {
		t.Run(strconv.Itoa(width), func(t *testing.T) { reviewNarrowRecovery(t, width) })
	}
}

func reviewNarrowRecovery(t *testing.T, width int) {
	t.Helper()
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded, acceptanceFailure: "malformed"}
	m := reviewConnected(t, h)
	keys := 0
	m.newKey = func() (string, error) { keys++; return "review-original-key", nil }
	m, cmd := issueForTest(m)
	m, _ = updateTUI(m, cmd())
	original := m.issued
	m, _ = updateTUI(m, keyMsg("esc"))
	m, _ = updateTUI(m, keyMsg("u"))
	m, _ = updateTUI(m, tea.WindowSizeMsg{Width: width, Height: 8})
	m, _ = updateTUI(m, keyMsg("a"))
	fits := m.frame().NoticeVisible(m.reviewScreen())
	m, cmd = updateTUI(m, keyMsg("y"))
	if !fits && (cmd != nil || h.applies != 1) {
		t.Fatal("clipped recovery confirmation issued")
	}
	if fits {
		if cmd == nil {
			t.Fatal("complete recovery confirmation blocked")
		}
		m, _ = updateTUI(m, cmd())
	}
	if m.issued != original || keys != 1 || original.Review.Key != "review-original-key" {
		t.Fatal("narrow recovery replaced identity")
	}
}

func TestReviewU4LateMalformedAfterRealTargetNavigation(t *testing.T) {
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded, acceptanceFailure: "malformed"}
	deps := wireDependencies(t, h)
	deps.LoadOperationTarget = func(dir, name string) (transport.Target, error) {
		if dir != lifecycleSelection.configDir {
			t.Fatal("config selection changed")
		}
		return transport.Target{Name: name}, nil
	}
	m := fixtureSession(t.Context(), fixtureHost(), "")
	m.configDir = lifecycleSelection.configDir
	m.connect = func(flags operationFlags) tuiHost { return connectedHost{flags: flags, deps: deps} }
	m = start(m)
	m, _ = press(m, "enter", "enter", "p", "enter")
	keys := 0
	m.newKey = func() (string, error) { keys++; return "review-original-key", nil }
	m, apply := issueForTest(m)
	original := m.issued
	m, _ = press(m, "esc", "esc", "esc", "esc", "down", "enter", "enter", "p")
	if m.flags.target != "fixture-pi" || m.issued != original {
		t.Fatalf("navigation failed: %s", m.flags.target)
	}
	m, _ = updateTUI(m, acceptMsg{requestID: original.RequestID + 1, accepted: jobs.Accepted{Status: "accepted", OperationID: "crossed"}})
	if original.Accepted != nil {
		t.Fatal("crossed issued request accepted")
	}
	m, _ = updateTUI(m, apply())
	if original.Err == nil || original.Err.Code() != result.TransportInvalidResponse {
		t.Fatal("late malformed acceptance discarded")
	}
	m, _ = press(m, "esc", "u")
	m, cmd := issueForTest(m)
	if cmd == nil {
		t.Fatal("explicit same-key recovery missing")
	}
	m, _ = updateTUI(m, cmd())
	if m.flags.target != "fixture-pi" || m.issued != original || original.Accepted == nil || keys != 1 {
		t.Fatal("recovery replaced navigation or identity")
	}
	count := 0
	for _, req := range h.requests {
		if strings.Contains(req, " lifecycle ") {
			count++
		}
	}
	if count != 1 || h.applies != 2 || h.requests[1] != h.requests[len(h.requests)-1] {
		t.Fatalf("replanned or changed original selection/key: %v", h.requests)
	}
}

func TestReviewU4StopMenuDoesNotPromiseUnchangedRoute(t *testing.T) {
	m := start(fixtureModel(fixtureHost(), "fixture"))
	m, _ = press(m, "enter", "p")
	if strings.Contains(view(m), "stop app; route and data unchanged") {
		t.Fatal("unconditional stop menu promises unchanged route, but managed StopApp withdraws its route")
	}
}

func TestReviewU4NoOperationDoesNotAdvertiseEvents(t *testing.T) {
	h := fixtureHost()
	h.apps = func(context.Context) (apps.Report, error) {
		report := fixtureReport()
		report.Apps[0].LastOperation = nil
		return report, nil
	}
	m := start(fixtureModel(h, "fixture"))
	m, _ = press(m, "enter")
	if strings.Contains(view(m), "t events") {
		t.Fatal("app without a last operation advertises an inapplicable t events key")
	}
}

func TestLifecycleJSONRejectsCrossedAction(t *testing.T) {
	for _, lifecycle := range []plan.ChangeKind{plan.RemoveApp, plan.StopApp, ""} {
		t.Run(string(lifecycle), func(t *testing.T) { testJSONCrossedLifecycle(t, lifecycle) })
	}
}

func testJSONCrossedLifecycle(t *testing.T, lifecycle plan.ChangeKind) {
	t.Helper()
	h := &lifecycleWireHost{t: t, action: actionRestart, final: ops.Succeeded}
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op == "lifecycle" {
			return reviewWire(t, req.Op, apps.ConfigPlan{PlanID: lifecyclePlanID, Kind: plan.Update, Lifecycle: lifecycle, Conflicts: []plan.Diagnostic{}})
		}
		return h.Call(ctx, target, req)
	})
	_, err := machineJSON(t, wireDependencies(t, client), "restart", "fixture-api")
	if result.Classify(err).Code() != result.TransportInvalidResponse || h.applies != 0 {
		t.Fatal("machine command accepted a crossed lifecycle plan", err, h.applies)
	}
}
