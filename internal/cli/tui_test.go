package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/ShaulLavo/brine/internal/ui"
)

// runTUIStub adapts a runner result for command-wiring tests that never start a terminal.
func runTUIStub(run func() error) func(context.Context, tea.Model, io.Reader, io.Writer) (tea.Model, error) {
	return func(_ context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		return model, run()
	}
}

// fakeTUIHost records each read and answers it from a per-method function.
type fakeTUIHost struct {
	mu         sync.Mutex
	calls      []string
	apps       func(context.Context) (apps.Report, error)
	logs       func(context.Context, string) ([]logs.Line, error)
	operation  func(context.Context, string, uint64) (jobs.Status, error)
	plan       func(context.Context, tuiAction, string) (tuiReview, error)
	apply      func(context.Context, string, string) (jobs.Accepted, error)
	deployment func(context.Context, string) (tuiReview, error)
	diagnose   func(context.Context, string) (diagnose.Report, error)
}

func (h *fakeTUIHost) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}

func (h *fakeTUIHost) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

func (h *fakeTUIHost) Apps(ctx context.Context) (apps.Report, error) {
	h.record("apps")
	return h.apps(ctx)
}

func (h *fakeTUIHost) Logs(ctx context.Context, app string) ([]logs.Line, error) {
	h.record("logs " + app)
	return h.logs(ctx, app)
}

func (h *fakeTUIHost) Operation(ctx context.Context, id string, cursor uint64) (jobs.Status, error) {
	h.record(fmt.Sprintf("operation %s %d", id, cursor))
	if h.operation != nil {
		return h.operation(ctx, id, cursor)
	}
	op := *fixtureReport().Apps[0].LastOperation
	return jobs.Status{Operation: op, Events: []ops.Event{}, NextCursor: cursor}, nil
}

func fixtureHost() *fakeTUIHost {
	return &fakeTUIHost{
		apps: func(context.Context) (apps.Report, error) { return fixtureReport(), nil },
		logs: func(context.Context, string) ([]logs.Line, error) { return fixtureLogLines(), nil },
	}
}

var fixtureReadTime = time.Date(2026, 11, 6, 12, 4, 31, 0, time.UTC)

// fixtureModel is the synthetic test session: target fixture, apps fixture-api,
// fixture-web and fixture-jobs. None of it exists outside tests.
func fixtureModel(host tuiHost, target string) tuiModel {
	return fixtureSession(context.Background(), host, target)
}

func fixtureSession(ctx context.Context, host tuiHost, target string) tuiModel {
	m := tuiModel{
		ctx:         ctx,
		connect:     func(operationFlags) tuiHost { return host },
		listTargets: func() ([]string, error) { return []string{"fixture", "fixture-pi"}, nil },
		width:       80,
		height:      24,
		theme:       ui.NewTheme(true),
		newKey:      randomOperationKey,
		now:         func() time.Time { return fixtureReadTime },
		loc:         time.UTC,
	}
	return m.begin(operationFlags{target: target})
}

func fixtureDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func fixtureReport() apps.Report {
	release := func(id, plan, image string) *apps.Release {
		return &apps.Release{ID: id, PlanID: fixtureDigest(plan), ImageDigest: fixtureDigest(image), Port: 20041, Domains: []spec.Domain{"api.fixture.test"}}
	}
	operation := func(id string, state ops.State) *ops.Operation {
		at := time.Date(2026, 11, 6, 12, 1, 7, 0, time.UTC)
		return &ops.Operation{ID: id, Kind: ops.Deploy, App: "fixture-api", PlanID: fixtureDigest("5"), State: state, CreatedAt: at, UpdatedAt: at}
	}
	return apps.Report{Apps: []apps.Status{
		{App: "fixture-api", Current: release("rel-7f3a91c2", "5", "a"), Previous: release("rel-3c02aa10", "9", "b"), LastOperation: operation("op-01fixture", ops.Succeeded), Health: apps.Health{UnitActive: target.Known(true), Direct: "healthy"}, Drift: apps.Drift{State: "in_sync", Fields: []string{}}},
		{App: "fixture-web", Current: release("rel-0b44e1d9", "7", "c"), Previous: release("rel-1a2b3c4d", "8", "d"), LastOperation: operation("op-02fixture", ops.Starting), Health: apps.Health{UnitActive: target.Known(true), Direct: "healthy"}, Drift: apps.Drift{State: "in_sync", Fields: []string{}}},
		{App: "fixture-jobs", Current: release("rel-91ad03fe", "6", "e"), LastOperation: operation("op-03fixture", ops.RecoveryRequired), Health: apps.Health{UnitActive: target.Observation[bool]{Status: target.Unknown}, Direct: "not_checked"}, Drift: apps.Drift{State: "drifted", Fields: []string{"image"}}},
	}}
}

func fixtureLogLines() []logs.Line {
	return []logs.Line{
		{Timestamp: "2026-11-06T12:04:27Z", Priority: 6, Message: "listening on :8080"},
		{Timestamp: "2026-11-06T12:04:28Z", Priority: 6, Message: "GET /health 200"},
		{Timestamp: "2026-11-06T12:04:29Z", Priority: 4, Message: "retrying upstream fixture-db: token=[REDACTED]"},
		{Timestamp: "2026-11-06T12:04:30Z", Priority: 6, Message: "request with multi-line body\nsecond line shown escaped"},
	}
}

// step applies msg and runs each returned command synchronously, feeding its
// message back. Commands are always single, so no batch driver is needed.
func step(m tuiModel, msg tea.Msg) (tuiModel, bool) {
	for msg != nil {
		if _, ok := msg.(tea.QuitMsg); ok {
			return m, true
		}
		next, cmd := m.Update(msg)
		m = next.(tuiModel)
		if cmd == nil {
			return m, false
		}
		msg = cmd()
	}
	return m, false
}

func start(m tuiModel) tuiModel {
	m, _ = step(m, m.Init()())
	return m
}

func press(m tuiModel, keys ...string) (tuiModel, bool) {
	for _, key := range keys {
		var quit bool
		if m, quit = step(m, keyMsg(key)); quit {
			return m, true
		}
	}
	return m, false
}

func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(key)[0]
	return tea.KeyPressMsg{Code: r, Text: key}
}

func view(m tuiModel) string { return m.View().Content }

func TestTUINavigationAndBackStack(t *testing.T) {
	host := fixtureHost()
	m := start(fixtureModel(host, ""))
	if m.screen != screenTargets || !strings.Contains(view(m), "› fixture") || !strings.Contains(view(m), "2 enrolled targets") {
		t.Fatalf("target picker: %q", view(m))
	}
	m, _ = press(m, "down", "up", "enter")
	if m.screen != screenApps || m.flags.target != "fixture" || !strings.Contains(view(m), "brine · fixture") || !strings.Contains(view(m), "3 apps · read 12:04:31") {
		t.Fatalf("apps: %q", view(m))
	}
	m, _ = press(m, "down", "down", "down", "enter")
	if m.screen != screenApp || m.app != "fixture-jobs" {
		t.Fatalf("app %q on screen %d", m.app, m.screen)
	}
	m, _ = press(m, "l")
	if m.screen != screenLogs || !strings.Contains(view(m), "tail 100 · read 12:04:31") {
		t.Fatalf("logs: %q", view(m))
	}
	for _, want := range []tuiScreen{screenApp, screenApps, screenTargets} {
		m, _ = press(m, "esc")
		if m.screen != want {
			t.Fatalf("back reached %d, want %d", m.screen, want)
		}
	}
	if m, quit := press(m, "esc"); quit || m.screen != screenTargets {
		t.Fatal("esc on the first screen must do nothing")
	}
	if got := strings.Join(host.Calls(), ","); got != "apps,logs fixture-jobs" {
		t.Fatalf("host reads = %s", got)
	}
}

func TestTUIQuitKeys(t *testing.T) {
	m := start(fixtureModel(fixtureHost(), "fixture"))
	if m.screen != screenApps || len(m.back) != 0 {
		t.Fatal("--target must start on the app list")
	}
	if _, quit := press(m, "q"); !quit {
		t.Fatal("q on the first screen must quit")
	}
	inner, _ := press(m, "enter")
	if after, quit := press(inner, "q"); quit || after.screen != screenApps {
		t.Fatal("q on an inner screen must go back")
	}
	if _, quit := press(inner, "ctrl+c"); !quit {
		t.Fatal("ctrl+c must quit anywhere")
	}
	if _, quit := step(m, tea.KeyReleaseMsg{Code: 'q', Text: "q"}); quit {
		t.Fatal("key release must not quit")
	}
}

// blockingHost holds every read open until its context ends, then reports
// cancellation, so tests can see that obsolete reads are really canceled.
type blockingHost struct {
	fakeTUIHost
	started  chan string
	canceled chan string
}

func newBlockingHost() *blockingHost {
	h := &blockingHost{started: make(chan string, 16), canceled: make(chan string, 16)}
	wait := func(ctx context.Context, name string) error {
		h.started <- name
		<-ctx.Done()
		h.canceled <- name
		return ctx.Err()
	}
	h.apps = func(ctx context.Context) (apps.Report, error) { return apps.Report{}, wait(ctx, "apps") }
	h.logs = func(ctx context.Context, app string) ([]logs.Line, error) { return nil, wait(ctx, "logs "+app) }
	return h
}

func receive(t *testing.T, ch chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func TestTUIStaleReadDroppedAndCanceled(t *testing.T) {
	host := fixtureHost()
	m := start(fixtureModel(host, "fixture"))
	m, _ = press(m, "enter", "l")
	before := m.logs

	// A log read is in flight; leaving the screen cancels it and drops its late reply.
	blocking := newBlockingHost()
	m.host = blocking
	m, cmd := model(m.refresh())
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	receive(t, blocking.started, "logs fixture-api")
	m, _ = model(m.goBack())
	receive(t, blocking.canceled, "logs fixture-api")
	m, _ = step(m, <-done)
	if m.screen != screenApp || m.logs.State != loadReady || len(m.logs.Value) != len(before.Value) {
		t.Fatalf("late log reply changed state: %+v", m.logs)
	}

	// A stale apps reply with an old token is dropped.
	stale := appsMsg{token: m.token - 1, report: apps.Report{Apps: []apps.Status{}}}
	if after, _ := step(m, stale); len(after.apps.Value.Apps) != 3 {
		t.Fatal("stale apps reply replaced the list")
	}

	// Refreshing twice leaves one current read: the first is canceled.
	first, cmd := model(m.refresh())
	go cmd()
	receive(t, blocking.started, "apps")
	second, cmd := model(first.refresh())
	receive(t, blocking.canceled, "apps")
	if second.token == first.token || cmd == nil || second.apps.State != loadLoading {
		t.Fatal("refresh did not replace the read")
	}
	second.cancelRead()
}

func model(m tea.Model, cmd tea.Cmd) (tuiModel, tea.Cmd) { return m.(tuiModel), cmd }

func TestTUIFailureKeepsLastGoodAndShowsCatalogOnly(t *testing.T) {
	host := fixtureHost()
	m := start(fixtureModel(host, "fixture"))
	host.apps = func(context.Context) (apps.Report, error) {
		return apps.Report{}, result.TransportError(errors.New("ssh: secret-host 10.0.0.9 refused"), result.TransportDiagnostics{Operation: "status", Reason: "exit", SSHStderrTail: "identity /home/x/.ssh/id"})
	}
	m, _ = press(m, "r")
	got := view(m)
	if !strings.Contains(strings.ReplaceAll(got, "\n", " "), "Error: transport_failure. The SSH transport failed; reconcile before retrying any mutation.") || !strings.Contains(got, "fixture-jobs") {
		t.Fatalf("failure view: %q", got)
	}
	for _, leaked := range []string{"secret-host", "10.0.0.9", "id_", ".ssh"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("view leaked %q: %q", leaked, got)
		}
	}
}

func TestListTargetsReadsNamesOnly(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"fixture.json":    "not json at all",
		"fixture-pi.json": `{"name":"other"}`,
		"Bad.json":        "{}",
		"notes.txt":       "{}",
		"-x.json":         "{}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "pins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	// An unreadable file is still listed: listing never opens target files.
	if err := os.Chmod(filepath.Join(dir, "fixture.json"), 0); err != nil {
		t.Fatal(err)
	}
	names, err := listTargets(operationFlags{configDir: dir})
	if err != nil || !slices.Equal(names, []string{"fixture", "fixture-pi"}) {
		t.Fatalf("names = %q, err = %v", names, err)
	}
	if names, err := listTargets(operationFlags{configDir: filepath.Join(dir, "missing")}); err != nil || names == nil || len(names) != 0 {
		t.Fatalf("missing directory: %q, %v", names, err)
	}
}

func TestTUITargetSelectionBeforeTerminal(t *testing.T) {
	for _, args := range [][]string{
		{"tui", "--target", "Bad Name"},
		{"tui", "--target", ""},
		{"tui", "--target", "fixture", "--config-dir", "dir\x1b[31m"},
		{"tui", "--config-dir", "dir" + string(rune(0x202e))}, // right-to-left override
	} {
		var out, diagnostics bytes.Buffer
		deps := testDependencies(t, &out, &diagnostics)
		err := Execute(deps, args)
		if result.Classify(err).Code() != result.InvalidUsage || result.ExitCode(err) != 2 {
			t.Fatalf("%q: error = %v", args, err)
		}
	}
	// Machine flags are still refused first, even with an invalid target.
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	if err := Execute(deps, []string{"tui", "--json", "--target", "Bad Name"}); result.Classify(err).Code() != result.TUIInteractive {
		t.Fatalf("error = %v", err)
	}
}

func TestTUIBadTargetFileRefusedBeforeRunner(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	withTestTerminal(t, &deps)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Execute(deps, []string{"tui", "--target", "fixture", "--config-dir", dir})
	if result.Classify(err).Code() != result.TransportInvalidTarget {
		t.Fatalf("error = %v", err)
	}
	// testDependencies fails the test if the runner starts.
}

func TestTUIStartsOnAppsForTarget(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	withTestTerminal(t, &deps)
	deps.LoadOperationTarget = func(dir, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	var started tuiModel
	deps.RunTUI = func(_ context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		started = model.(tuiModel)
		return model, nil
	}
	if err := Execute(deps, []string{"tui", "--target", "fixture"}); err != nil {
		t.Fatal(err)
	}
	if started.screen != screenApps || started.flags.target != "fixture" || started.apps.State != loadLoading || started.Init() == nil {
		t.Fatalf("model did not start on the fixture app list: screen %d", started.screen)
	}
}

// TestTUILogRedaction plants secrets and terminal controls in host log output.
// They pass through the real dispatcher decode and the TUI's shared read path.
func TestTUILogRedaction(t *testing.T) {
	planted := []map[string]any{
		{"timestamp": "2026-11-06T12:04:27Z", "priority": 4, "message": "login password=hunter2 ok"},
		{"timestamp": "2026-11-06T12:04:28Z", "priority": 4, "message": "Authorization: Bearer abc.def.ghi"},
		{"timestamp": "2026-11-06T12:04:29Z", "priority": 4, "message": "db postgres://app:s3cr3tpw@db.fixture.test/app"}, // #nosec G101 -- Planted fake credential the view must redact.
		{"timestamp": "2026-11-06T12:04:30Z", "priority": 4, "message": "copy \x1b]52;c;c2VjcmV0\x07 then \x1b[2J\x1b[31mred\u009b1m"},
		{"timestamp": "2026-11-06T12:04:31Z", "priority": 4, "message": "line one\nline two\rcarriage"},
	}
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(dir, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	var requests []dispatch.Request
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		req.Args = slices.Clone(req.Args) // call clears its argument buffer after the request
		requests = append(requests, req)
		raw, err := json.Marshal(result.Success("brine host logs", planted))
		if err != nil {
			t.Fatal(err)
		}
		return dispatch.DecodeResponse(raw, req.Op)
	})
	host := connectedHost{flags: operationFlags{target: "fixture"}, deps: deps}
	m := fixtureModel(host, "fixture")
	m.apps.finish(fixtureReport(), nil, fixtureReadTime)
	m.app = "fixture-api"
	m.screen = screenApp
	m, _ = press(m, "l")
	if m.logs.State != loadReady || len(requests) != 1 || requests[0].Op != "logs" || string(requests[0].Args) != `{"app":"fixture-api","tail":100,"since":""}` {
		t.Fatalf("log read: %+v %+v", m.logs, requests)
	}
	for _, noColor := range []bool{true, false} {
		m.theme = ui.NewTheme(noColor)
		for _, size := range []tea.WindowSizeMsg{{Width: 200, Height: 12}, {Width: 50, Height: 12}} {
			m, _ = step(m, size)
			checkRedacted(t, view(m), noColor)
		}
	}
	if !strings.Contains(view(m), `line one\nline twocarriage`) {
		t.Fatalf("newline was not escaped before sanitizing: %q", view(m))
	}
}

func checkRedacted(t *testing.T, got string, noColor bool) {
	t.Helper()
	for _, leaked := range []string{"hunter2", "abc.def.ghi", "s3cr3tpw", "c2VjcmV0", "\x1b]", "\x1b[2J", "\u009b", "\x07", "\r"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("view contains %q: %q", leaked, got)
		}
	}
	if noColor && strings.Contains(got, "\x1b") {
		t.Fatalf("no-color view contains ESC: %q", got)
	}
}

func TestReadLogLinesBounds(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.LoadOperationTarget = func(dir, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	lines := make([]map[string]any, 3)
	for i := range lines {
		lines[i] = map[string]any{"timestamp": "2026-11-06T12:04:27Z", "priority": 6, "message": "x"}
	}
	deps.OperationClient = callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		return result.Success("brine host logs", lines), nil
	})
	flags := operationFlags{target: "fixture"}
	if _, err := readLogLines(context.Background(), deps, flags, logs.Request{App: "fixture-api", Tail: 2}); result.Classify(err).Code() != result.TransportInvalidResponse {
		t.Fatalf("more lines than requested: %v", err)
	}
	if _, err := readLogLines(context.Background(), deps, flags, logs.Request{App: "Bad", Tail: 2}); result.Classify(err).Code() != result.InvalidUsage {
		t.Fatalf("invalid request: %v", err)
	}
	if got, err := readLogLines(context.Background(), deps, flags, logs.Request{App: "fixture-api", Tail: 3}); err != nil || len(got) != 3 {
		t.Fatalf("got %d, %v", len(got), err)
	}
}

func (h *fakeTUIHost) DeploymentPlan(ctx context.Context, path string) (tuiReview, error) {
	h.record("deployment " + path)
	return h.deployment(ctx, path)
}

func (h *fakeTUIHost) Diagnose(ctx context.Context, app string) (diagnose.Report, error) {
	h.record("diagnose " + app)
	return h.diagnose(ctx, app)
}
