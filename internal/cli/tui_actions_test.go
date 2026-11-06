package cli

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"fmt"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTUILifecycleActionEntry(t *testing.T) {
	m := start(fixtureModel(fixtureHost(), "fixture"))
	m, _ = press(m, "enter", "p")
	text := view(m)
	for _, want := range []string{"Choose a change to plan", "Restart", "Stop", "Start", "Roll back"} {
		if !strings.Contains(text, want) {
			t.Errorf("actions missing %q in %s", want, text)
		}
	}
}

func (h *fakeTUIHost) Plan(ctx context.Context, action tuiAction, app string) (tuiReview, error) {
	h.record("plan " + string(action) + " " + app)
	if h.plan != nil {
		return h.plan(ctx, action, app)
	}
	p := apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Lifecycle: plan.RestartApp, Conflicts: []plan.Diagnostic{}}
	return tuiReview{Action: action, App: app, PlanID: p.PlanID, Kind: p.Kind, Config: &p}, nil
}

func (h *fakeTUIHost) Apply(ctx context.Context, planID, key string) (jobs.Accepted, error) {
	h.record("apply " + planID + " " + key)
	if h.apply != nil {
		return h.apply(ctx, planID, key)
	}
	return jobs.Accepted{Status: "accepted", OperationID: "op-u4"}, nil
}

func reviewedModel(host *fakeTUIHost) tuiModel {
	m := start(fixtureModel(host, "fixture"))
	m, _ = press(m, "enter", "p", "enter")
	m.newKey = func() (string, error) { return "u4-fixed-key", nil }
	return m
}

func issueForTest(m tuiModel) (tuiModel, tea.Cmd) {
	m, _ = updateTUI(m, keyMsg("a"))
	return updateTUI(m, keyMsg("y"))
}

func TestTUILifecycleConfirmationAndBlockedPlans(t *testing.T) {
	for _, kind := range []plan.Kind{plan.Update, plan.Conflict, plan.NoOp} {
		t.Run(string(kind), func(t *testing.T) { testConfirmation(t, kind) })
	}
}

func testConfirmation(t *testing.T, kind plan.Kind) {
	t.Helper()
	h := fixtureHost()
	m := reviewedModel(h)
	m.review.Value.Kind, m.review.Value.Config.Kind = kind, kind
	m, _ = press(m, "y")
	if m.issued != nil {
		t.Fatal("y without a must not issue")
	}
	m, _ = press(m, "a", "n")
	if m.confirm || m.issued != nil {
		t.Fatal("non-y did not cancel confirmation")
	}
	m, cmd := issueForTest(m)
	if kind != plan.Update {
		if cmd != nil || m.issued != nil {
			t.Fatal("blocked plan issued")
		}
		return
	}
	if cmd == nil || m.issued == nil || m.issued.Review.Key != "u4-fixed-key" {
		t.Fatal("confirmed plan not issued")
	}
	if m.issued.Review.PlanID != m.review.Value.PlanID {
		t.Fatal("plan changed")
	}
	m, again := updateTUI(m, keyMsg("y"))
	if again != nil {
		t.Fatal("repeated y issued twice")
	}
	m, _ = updateTUI(m, cmd())
	if m.issued.Accepted == nil || m.screen != screenOperation {
		t.Fatal("acceptance did not hand off to observation")
	}
}

func TestTUILifecycleUnreadableConfirmationCannotApply(t *testing.T) {
	for _, width := range []int{1, 20, 30, 80} {
		m := reviewedModel(fixtureHost())
		m.width, m.height = width, 4
		m.review.Value.App = strings.Repeat("long-name-", 7)
		m.reviewFlags.target = strings.Repeat("target-", 9)
		m, _ = updateTUI(m, keyMsg("a"))
		if m.frame().NoticeVisible(m.reviewScreen()) {
			t.Fatal("test requires unreadable notice")
		}
		m, cmd := updateTUI(m, keyMsg("y"))
		if cmd != nil || m.issued != nil {
			t.Fatalf("unreadable confirmation issued at width %d", width)
		}
	}
}

func TestTUILifecycleIssuedIdentitySurvivesNavigationAndLateAccept(t *testing.T) {
	h := fixtureHost()
	m := reviewedModel(h)
	m.reviewFlags.configDir = "/fixture config's targets"
	m, apply := issueForTest(m)
	original := m.issued
	m, _ = updateTUI(m, keyMsg("esc"))
	m, _ = updateTUI(m, keyMsg("esc"))
	m.flags = operationFlags{target: "elsewhere", configDir: "other"}
	m, cmd := updateTUI(m, keyMsg("r"))
	if cmd != nil {
		m, _ = step(m, cmd())
	}
	m, _ = updateTUI(m, keyMsg("p"))
	if m.issued != original || len(h.Calls()) != 3 {
		t.Fatalf("pending identity replaced or planned: %v", h.Calls())
	}
	m, _ = updateTUI(m, keyMsg("esc"))
	m, cmd = updateTUI(m, apply())
	if cmd != nil || m.flags.target != "elsewhere" || m.issued.Accepted == nil {
		t.Fatal("late acceptance discarded or navigated target")
	}
	notice := m.exitNotice()
	if !strings.Contains(notice, "--target fixture") || !strings.Contains(notice, "config") || strings.Contains(notice, "--target elsewhere") {
		t.Fatalf("original selection lost: %s", notice)
	}
}

func TestTUILifecycleEveryPostIssueErrorRetainsExplicitSameKeyRecovery(t *testing.T) {
	for _, code := range []result.Code{result.TransportFailure, result.TransportInvalidResponse, result.InternalError, result.PolicyRefused} {
		t.Run(string(code), func(t *testing.T) { testSameKeyRecovery(t, code) })
	}
}

func testSameKeyRecovery(t *testing.T, code result.Code) {
	t.Helper()
	h := fixtureHost()
	h.apply = func(context.Context, string, string) (jobs.Accepted, error) {
		return jobs.Accepted{}, result.New(code, errors.New("password=hunter2"))
	}
	m := reviewedModel(h)
	keys := 0
	m.newKey = func() (string, error) { keys++; return "only-key", nil }
	m, cmd := issueForTest(m)
	m, next := updateTUI(m, cmd())
	if next != nil || m.issued == nil || m.issued.Err.Code() != code {
		t.Fatal("uncertain apply was retried or discarded")
	}
	m.flags.target = "elsewhere"
	m, _ = updateTUI(m, keyMsg("esc"))
	m, _ = updateTUI(m, keyMsg("u"))
	if !strings.Contains(m.exitNotice(), "--idempotency-key only-key --target fixture") {
		t.Fatal(m.exitNotice())
	}
	if strings.Contains(view(m), "hunter2") {
		t.Fatal("cause leaked")
	}
	m, cmd = issueForTest(m)
	if cmd == nil {
		t.Fatal("explicit recovery unavailable")
	}
	m, _ = updateTUI(m, cmd())
	if keys != 1 || m.issued.Review.Key != "only-key" {
		t.Fatal("recovery generated a new key")
	}
	calls := h.Calls()
	if len(calls) != 4 || calls[2] != calls[3] {
		t.Fatalf("replanned or changed recovery: %v", calls)
	}
}

func TestTUILifecycleOnlyMatchingTerminalObservationClearsIssued(t *testing.T) {
	for _, mismatch := range []string{"app", "plan", "target", "operation", "none"} {
		t.Run(mismatch, func(t *testing.T) { testTerminalIdentity(t, mismatch) })
	}
}

func testTerminalIdentity(t *testing.T, mismatch string) {
	t.Helper()
	m, cmd := issueForTest(reviewedModel(fixtureHost()))
	m, _ = updateTUI(m, cmd())
	status := operationFixture(ops.Succeeded, 0, 0)
	status.Operation.ID, status.Operation.PlanID = "op-u4", m.issued.Review.PlanID
	switch mismatch {
	case "app":
		status.Operation.App = "wrong"
	case "plan":
		status.Operation.PlanID = "sha256:" + strings.Repeat("b", 64)
	case "target":
		m.observe.Flags.target = "elsewhere"
	case "operation":
		m.observe.Operation.ID, status.Operation.ID = "other", "other"
	}
	m, _ = updateTUI(m, pageMsg{token: m.token, status: status})
	if (m.issued == nil) != (mismatch == "none") {
		t.Fatalf("incorrect settlement for %s", mismatch)
	}
}

func TestTUILifecycleApplyIsNotCanceledByReadNavigation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := fixtureHost()
	started, stopped := make(chan string, 1), make(chan string, 1)
	release := make(chan struct{})
	h.apply = func(ctx context.Context, _, _ string) (jobs.Accepted, error) {
		started <- "apply"
		select {
		case <-ctx.Done():
			stopped <- "canceled"
			return jobs.Accepted{}, ctx.Err()
		case <-release:
			return jobs.Accepted{Status: "accepted", OperationID: "op-u4"}, nil
		}
	}
	m := start(fixtureSession(ctx, h, ""))
	m, _ = press(m, "enter", "enter", "p", "enter")
	m.newKey = func() (string, error) { return "only-key", nil }
	m, cmd := issueForTest(m)
	id := m.issued.RequestID
	response := make(chan tea.Msg, 1)
	go func() { response <- cmd() }()
	receive(t, started, "apply")
	m, _ = press(m, "esc", "esc", "esc", "esc", "down", "enter", "r")
	if m.flags.target != "fixture-pi" {
		t.Fatalf("did not browse another target: %s", m.flags.target)
	}
	select {
	case <-stopped:
		t.Fatal("read navigation canceled session apply")
	default:
	}
	m, _ = updateTUI(m, acceptMsg{requestID: id + 1, accepted: jobs.Accepted{Status: "accepted", OperationID: "wrong"}})
	if m.issued.Accepted != nil {
		t.Fatal("unrelated acceptance matched")
	}
	close(release)
	var msg tea.Msg
	select {
	case msg = <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not return")
	}
	m, cmd = updateTUI(m, msg)
	if cmd != nil || m.flags.target != "fixture-pi" || m.issued.Accepted == nil {
		t.Fatal("late acceptance changed current target or was discarded")
	}
	m, _ = press(m, "enter", "p")
	if m.issued.RequestID != id || m.observe.Flags.target != "fixture" {
		t.Fatal("new plan replaced issued identity")
	}
}

func TestTUILifecycleReviewRedactionAndRollbackAvailability(t *testing.T) {
	h := fixtureHost()
	m := reviewedModel(h)
	m.review.Value.Config.Diff = &plan.ConfigurationDiff{Environment: &plan.EnvironmentChange{Changed: []string{"FIXTURE_MODE"}}, Secrets: []plan.SecretChange{{Environment: "FIXTURE_TOKEN", To: &plan.SecretVersion{Reference: "fixture-token", VersionName: "v2"}}}}
	text := view(m)
	for _, want := range []string{"FIXTURE_MODE: changed (value hidden)", "FIXTURE_TOKEN: fixture-token [v2]"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing value-free diff %q", want)
		}
	}
	m.apps.Value.Apps[0].Previous = nil
	if slices.Contains(m.actions(), actionRollback) {
		t.Fatal("offered unknown rollback release")
	}
}

func TestTUILifecycleKeyFailureDoesNotIssue(t *testing.T) {
	m := reviewedModel(fixtureHost())
	m.newKey = func() (string, error) { return "", errors.New("random source failed") }
	m, cmd := issueForTest(m)
	if cmd != nil || m.issued != nil || m.review.Err == nil {
		t.Fatal("failed key generation issued a mutation")
	}
}

func TestTUILifecycleTerminalFullPageCannotClearBeforeHistoryDrains(t *testing.T) {
	m, cmd := issueForTest(reviewedModel(fixtureHost()))
	m, _ = updateTUI(m, cmd())
	status := operationFixture(ops.Succeeded, 0, jobs.EventPageLimit)
	status.Operation.ID, status.Operation.PlanID = "op-u4", m.issued.Review.PlanID
	m, cmd = updateTUI(m, pageMsg{token: m.token, status: status})
	if cmd == nil || m.issued == nil || !strings.Contains(m.exitNotice(), "has not been fully observed") {
		t.Fatal("cleared issued identity before draining history or lost exit notice")
	}
	m, _ = press(m, "esc", "p")
	if m.issued == nil {
		t.Fatal("navigation lost undrained terminal identity")
	}
}

func TestTUILifecycleRecoveredObservationLogsUseOriginalSelection(t *testing.T) {
	original, other := fixtureHost(), fixtureHost()
	m, cmd := issueForTest(reviewedModel(original))
	m, _ = updateTUI(m, cmd())
	m, _ = updateTUI(m, keyMsg("esc"))
	m.flags, m.host, m.app = operationFlags{target: "elsewhere"}, other, "fixture-web"
	m.connect = func(flags operationFlags) tuiHost {
		if flags.target == "fixture" {
			return original
		}
		return other
	}
	m, _ = updateTUI(m, keyMsg("u"))
	m, _ = press(m, "l")
	if len(other.Calls()) != 0 || !slices.Contains(original.Calls(), "logs fixture-api") {
		t.Fatalf("recovered operation logs used current browsing selection: original %v other %v", original.Calls(), other.Calls())
	}
	if m.flags.target != "elsewhere" || m.app != "fixture-web" {
		t.Fatal("log navigation changed browsing selection")
	}
	if !strings.Contains(view(m), "brine · fixture › fixture-api › logs") {
		t.Fatal(view(m))
	}
}

func TestTUILifecycleAcceptanceShowsNoUnreadHostFacts(t *testing.T) {
	m, cmd := issueForTest(reviewedModel(fixtureHost()))
	m, _ = updateTUI(m, cmd())
	text := view(m)
	if strings.Contains(text, "started 00:00:00") || !strings.Contains(text, "Accepted. Reading operation state") {
		t.Fatal(text)
	}
}

func TestTUILifecycleReviewScrollReversesAfterResize(t *testing.T) {
	m := reviewedModel(fixtureHost())
	keys := make([]string, 60)
	for i := range keys {
		keys[i] = fmt.Sprintf("FIXTURE_%02d", i)
	}
	m.review.Value.Config.Diff = &plan.ConfigurationDiff{Environment: &plan.EnvironmentChange{Changed: keys}}
	m, _ = step(m, tea.WindowSizeMsg{Width: 30, Height: 8})
	for range 200 {
		m, _ = press(m, "down")
	}
	m, _ = step(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	bottom := view(m)
	m, _ = press(m, "up")
	if view(m) == bottom {
		t.Fatal("first Up after resize did not move review")
	}
}
