package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestTUIOperationEntryReadsLastOperation(t *testing.T) {
	for _, key := range []string{"t", "enter"} {
		t.Run(key, func(t *testing.T) { testOperationEntry(t, key) })
	}
}

func testOperationEntry(t *testing.T, key string) {
	t.Helper()
	host := fixtureHost()
	m := start(fixtureModel(host, "fixture"))
	m, _ = press(m, "enter", key)
	if !strings.Contains(view(m), "op-01fixture") || !strings.Contains(strings.Join(host.Calls(), ","), "operation op-01fixture 0") {
		t.Fatalf("last operation was not observed: calls %v, view %q", host.Calls(), view(m))
	}
}

func TestTUIAppDetailsReachHealthOnNarrowTerminal(t *testing.T) {
	m := start(fixtureModel(fixtureHost(), "fixture"))
	m, _ = press(m, "enter")
	m, _ = step(m, tea.WindowSizeMsg{Width: 30, Height: 8})
	seen := view(m)
	if !strings.Contains(seen, "↑↓ scroll") {
		t.Fatal("narrow detail view hides its scroll control")
	}
	for range 60 {
		m, _ = press(m, "down")
		seen += view(m)
	}
	for _, want := range []string{"Direct health", "healthy", "Last operation"} {
		if !strings.Contains(seen, want) {
			t.Errorf("cannot scroll to %q", want)
		}
	}
}

// updateTUI deliberately executes no command, so a read and its one-shot tick
// can each be asserted before the test advances the observation.
func updateTUI(m tuiModel, msg tea.Msg) (tuiModel, tea.Cmd) { return model(m.Update(msg)) }

func observing(host tuiHost) (tuiModel, tea.Cmd) {
	m := start(fixtureModel(host, "fixture"))
	m, _ = press(m, "enter")
	m.apps.Value.Apps[0].LastOperation.State = ops.Starting
	m.after = func(_ time.Duration, msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }
	return updateTUI(m, keyMsg("t"))
}

func operationFixture(state ops.State, cursor uint64, count int) jobs.Status {
	op := *fixtureReport().Apps[0].LastOperation
	op.State, op.UpdatedAt = state, fixtureReadTime
	events := make([]ops.Event, count)
	next := cursor
	for i := range events {
		next++
		events[i] = ops.Event{Sequence: next, Kind: "state", State: ops.Preflight, CreatedAt: fixtureReadTime}
	}
	return jobs.Status{Operation: op, Events: events, NextCursor: next}
}

func readTUI(t *testing.T, m tuiModel, cmd tea.Cmd) (tuiModel, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing read/tick command")
	}
	return updateTUI(m, cmd())
}

func TestTUIOperationNoLastOperation(t *testing.T) {
	host := fixtureHost()
	host.apps = func(context.Context) (apps.Report, error) {
		report := fixtureReport()
		report.Apps[0].LastOperation = nil
		return report, nil
	}
	m := start(fixtureModel(host, "fixture"))
	m, _ = press(m, "enter", "t", "enter")
	if m.screen != screenApp || !slices.Equal(host.Calls(), []string{"apps"}) {
		t.Fatalf("calls %v, screen %d", host.Calls(), m.screen)
	}
}

func TestTUIOperationDrainsPagesBeforeTerminalAndRefreshesOnce(t *testing.T) {
	host := fixtureHost()
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		count := 128
		if cursor >= 256 {
			count = 1
		}
		return operationFixture(ops.Succeeded, cursor, count), nil
	}
	m, cmd := observing(host)
	for i, cursor := range []uint64{128, 256} {
		m, cmd = readTUI(t, m, cmd)
		if m.observe.Cursor != cursor || m.observe.Phase == observeSettled || len(host.Calls()) != i+2 {
			t.Fatalf("settled before draining: %+v calls %v", m.observe, host.Calls())
		}
	}
	m, cmd = readTUI(t, m, cmd)
	if m.observe.Phase != observeSettled || len(m.observe.Events) != 257 || cmd == nil {
		t.Fatalf("not settled: %+v", m.observe)
	}
	m, cmd = readTUI(t, m, cmd)
	if cmd != nil || !slices.Equal(host.Calls(), []string{"apps", "operation op-01fixture 0", "operation op-01fixture 128", "operation op-01fixture 256", "apps"}) {
		t.Fatalf("calls %v", host.Calls())
	}
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		return operationFixture(ops.Succeeded, cursor, 0), nil
	}
	m, cmd = updateTUI(m, keyMsg("r"))
	m, cmd = readTUI(t, m, cmd)
	if cmd != nil || !m.observe.Refreshed || m.exitNotice() != "" {
		t.Fatalf("settlement refreshed twice or left exit notice: %v", host.Calls())
	}
}

func TestTUILaunchUnknownPausesAfterDrainAndManualReadResumes(t *testing.T) {
	host := fixtureHost()
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		count := 0
		if cursor == 0 {
			count = 128
		}
		return operationFixture(ops.LaunchUnknown, cursor, count), nil
	}
	m, cmd := observing(host)
	m.observe.Flags.configDir = "/fixture/private configs/'quoted'"
	m, cmd = readTUI(t, m, cmd)
	if m.observe.Phase == observeUncertain || cmd == nil {
		t.Fatal("launch uncertainty paused before history was drained")
	}
	m, cmd = readTUI(t, m, cmd)
	if cmd != nil || m.observe.Phase != observeUncertain || m.observe.Operation.State.IsTerminal() || m.observe.Refreshed {
		t.Fatalf("uncertainty falsely settled: %+v", m.observe)
	}
	notice := m.exitNotice()
	if !strings.Contains(notice, "do not relaunch") || !strings.Contains(notice, "brine reconcile --dry-run --target fixture") || !strings.Contains(notice, "--after-cursor 128") {
		t.Fatalf("exit notice %q", notice)
	}
	if !strings.Contains(strings.Join(m.operationLines(), " "), "--config-dir '/fixture/private configs/'\"'\"'quoted'\"'\"''") {
		t.Fatalf("missing original config quoting: %v", m.operationLines())
	}
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		return operationFixture(ops.Starting, cursor, 1), nil
	}
	m, cmd = updateTUI(m, keyMsg("r"))
	m, cmd = readTUI(t, m, cmd)
	if cmd == nil || m.observe.Cursor != 129 || m.observe.Phase != observePolling {
		t.Fatalf("manual resume: %+v", m.observe)
	}
	m, _ = updateTUI(m, keyMsg("esc"))
	m.flags = operationFlags{target: "other", configDir: "/other"}
	if got := m.exitNotice(); strings.Contains(got, "--target other") || !strings.Contains(got, "--target fixture") {
		t.Fatalf("recovery context changed with navigation: %q", got)
	}
}

func TestTUIOperationDisconnectBackoffPauseAndResume(t *testing.T) {
	host := fixtureHost()
	calls := 0
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		calls++
		if calls > 1 && calls < 5 {
			return jobs.Status{}, result.TransportError(errors.New("secret fixture stderr"), result.TransportDiagnostics{})
		}
		return operationFixture(ops.Starting, cursor, 9), nil
	}
	m, cmd := observing(host)
	var delays []time.Duration
	m.after = func(delay time.Duration, msg tea.Msg) tea.Cmd {
		delays = append(delays, delay)
		return func() tea.Msg { return msg }
	}
	m, cmd = readTUI(t, m, cmd)
	for range 3 {
		m, cmd = readTUI(t, m, cmd) // tick begins one read
		m, cmd = readTUI(t, m, cmd) // failed read schedules at most one tick
	}
	if cmd != nil || m.observe.Phase != observePaused || m.observe.Cursor != 9 || m.observe.Failures != 3 {
		t.Fatalf("pause: %+v", m.observe)
	}
	if !slices.Equal(delays, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatalf("delays %v", delays)
	}
	if strings.Contains(view(m), "secret fixture stderr") || !strings.Contains(strings.Join(strings.Fields(view(m)), " "), "r resumes from event 9") {
		t.Fatalf("pause view %q", view(m))
	}
	m, cmd = updateTUI(m, keyMsg("r"))
	m, cmd = readTUI(t, m, cmd)
	if m.observe.Cursor != 18 || m.observe.Failures != 0 || m.observe.Phase != observePolling || cmd == nil {
		t.Fatalf("resume: %+v", m.observe)
	}
	if !slices.Equal(host.Calls(), []string{"apps", "operation op-01fixture 0", "operation op-01fixture 9", "operation op-01fixture 9", "operation op-01fixture 9", "operation op-01fixture 9"}) {
		t.Fatalf("calls %v", host.Calls())
	}
	m.stopRead()
}

func TestTUIOperationOtherErrorsPauseWithoutRetry(t *testing.T) {
	host := fixtureHost()
	host.operation = func(context.Context, string, uint64) (jobs.Status, error) {
		return jobs.Status{}, result.New(result.AppNotFound, errors.New("private cause"))
	}
	m, cmd := observing(host)
	m, cmd = readTUI(t, m, cmd)
	if cmd != nil || m.observe.Phase != observePaused || m.observe.Failures != 0 || !strings.Contains(view(m), "app_not_found") || strings.Contains(view(m), "private cause") {
		t.Fatalf("nontransport error: %+v %q", m.observe, view(m))
	}
}

func TestTUIOperationRetentionTruthfulAndScrollable(t *testing.T) {
	host := fixtureHost()
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		count := 128
		if cursor >= 1152 {
			count = 1
		}
		return operationFixture(ops.RecoveryRequired, cursor, count), nil
	}
	m, cmd := observing(host)
	for range 10 {
		m, cmd = readTUI(t, m, cmd)
	}
	if len(m.observe.Events) != 1024 || m.observe.Dropped != 129 || m.observe.Events[0].Sequence != 130 || m.observe.Cursor != 1153 {
		t.Fatalf("retention %+v", m.observe)
	}
	lines := strings.Join(m.operationLines(), " ")
	if !strings.Contains(lines, "129 older events not retained") || strings.Contains(lines, "brine resolve") || !strings.Contains(lines, "supported automatic resolution is not established") {
		t.Fatalf("truncated history misrepresented: %s", lines)
	}
	m.width, m.height = 30, 8
	for range 2000 {
		m = m.move(1)
	}
	if !strings.Contains(view(m), "established") {
		t.Fatalf("cannot reach recovery guidance: %q", view(m))
	}
	m.stopRead()
}

func TestTUIOperationTerminalStatesAndTaskOutcome(t *testing.T) {
	for _, state := range []ops.State{ops.Succeeded, ops.Failed, ops.RolledBack, ops.RecoveryRequired} {
		t.Run(string(state), func(t *testing.T) { testTerminalObservation(t, state) })
	}
}

func testTerminalObservation(t *testing.T, state ops.State) {
	t.Helper()
	host := fixtureHost()
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		status := operationFixture(state, cursor, 1)
		if state == ops.Failed {
			status.Operation.Kind, status.Operation.PlanID, status.Operation.SecretRef = ops.NativeCapture, "", "fixture-capture"
			status.Outcome = &ops.TaskOutcome{Error: result.InternalError}
		}
		return status, nil
	}
	m, cmd := observing(host)
	m, cmd = readTUI(t, m, cmd)
	if m.observe.Phase != observeSettled || cmd == nil || m.exitNotice() != "" {
		t.Fatalf("terminal state %s not settled", state)
	}
	m, cmd = readTUI(t, m, cmd)
	if cmd != nil || !strings.Contains(view(m), words(string(state))) {
		t.Fatalf("terminal view %s", view(m))
	}
	if state == ops.Failed && !strings.Contains(view(m), "Outcome: internal_error") {
		t.Fatalf("missing task outcome %s", view(m))
	}
}

func TestTUIOperationReturningFromLogsResumesRetainedCursor(t *testing.T) {
	host := fixtureHost()
	host.operation = func(_ context.Context, _ string, cursor uint64) (jobs.Status, error) {
		return operationFixture(ops.Starting, cursor, 1), nil
	}
	m, cmd := observing(host)
	m, _ = readTUI(t, m, cmd)
	m, _ = press(m, "l")
	if m.screen != screenLogs {
		t.Fatal("operation logs did not open")
	}
	m, cmd = updateTUI(m, keyMsg("esc"))
	if m.screen != screenOperation {
		t.Fatal("logs did not return to operation")
	}
	m, _ = readTUI(t, m, cmd)
	if m.observe.Cursor != 2 || !slices.Equal(host.Calls(), []string{"apps", "operation op-01fixture 0", "logs fixture-api", "operation op-01fixture 1"}) {
		t.Fatalf("cursor reset on return: %v", host.Calls())
	}
	m.stopRead()
}

func TestTUIAppDetailScrollBoundsAfterResizeAndReadError(t *testing.T) {
	m := start(fixtureModel(fixtureHost(), "fixture"))
	m, _ = press(m, "enter")
	m.width, m.height = 30, 8
	m.apps.Err = result.New(result.TransportFailure, nil)
	seen := ""
	for range 80 {
		seen += view(m)
		m = m.move(1)
	}
	if !strings.Contains(seen, "healthy") || !strings.Contains(seen, "in_sync") {
		t.Fatalf("detail error hid health/drift: %s", seen)
	}
	m, _ = step(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.detailOffset != 0 || !strings.Contains(view(m), "healthy") {
		t.Fatalf("resize did not bound scroll: %d", m.detailOffset)
	}
	for range 80 {
		m = m.move(-1)
	}
	if m.detailOffset != 0 {
		t.Fatal("negative detail scroll")
	}
}
