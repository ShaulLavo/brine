package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func directOperationModel(t *testing.T, client OperationClient) tuiModel {
	t.Helper()
	m := newTUIModel(t.Context(), wireDependencies(t, client), lifecycleSelection, tuiEntry{Operation: "op-direct"})
	m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	return start(m)
}

func TestTUIOperationEntryWithoutCommittedApp(t *testing.T) {
	for _, state := range []ops.State{ops.Queued, ops.Failed} {
		t.Run(string(state), func(t *testing.T) { checkOperationWithoutApp(t, state) })
	}
	t.Run("empty reconcile identity", testDirectReconcile)
	t.Run("runner", testDirectOperationRunner)
}

func checkOperationWithoutApp(t *testing.T, state ops.State) {
	t.Helper()
	calls := 0
	m := directOperationModel(t, callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		calls++
		if req.Op != "operation" {
			t.Fatalf("direct entry read %s", req.Op)
		}
		page := operationFixture(state, 0, 0)
		page.Operation.ID, page.Operation.App = "op-direct", "uncommitted"
		return reviewWire(t, req.Op, page)
	}))
	if calls != 1 || m.screen != screenOperation || !m.observe.Bound || m.observe.Operation.App != "uncommitted" || !strings.Contains(view(m), string(state)) {
		t.Fatalf("entry failed: %+v", m.observe)
	}
	if state.IsTerminal() && (m.observe.Phase != observeSettled || m.exitNotice() != "") {
		t.Fatal("drained terminal not settled")
	}
}

func testDirectReconcile(t *testing.T) {
	m := directOperationModel(t, callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		page := operationFixture(ops.Succeeded, 0, 0)
		page.Operation.ID, page.Operation.App, page.Operation.PlanID, page.Operation.Kind = "op-direct", "", "", ops.Reconcile
		return reviewWire(t, req.Op, page)
	}))
	if m.observe.Err != nil || !m.observe.Bound || m.observe.Operation.Kind != ops.Reconcile || m.observe.Phase != observeSettled {
		t.Fatalf("assumed deployment: %+v", m.observe)
	}
}

func TestTUIOperationEntryIdentityBinding(t *testing.T) {
	for _, crossed := range []string{"app", "plan", "kind"} {
		t.Run(crossed, func(t *testing.T) { checkDirectIdentity(t, crossed) })
	}
}

func checkDirectIdentity(t *testing.T, crossed string) {
	t.Helper()
	calls := 0
	client := callFunc(func(_ context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "operation" || target.Name != lifecycleSelection.target {
			t.Fatalf("crossed observation scope: %s %s", target.Name, req.Op)
		}
		calls++
		var args dispatch.OperationArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			t.Fatal(err)
		}
		if calls > 1 && args.AfterCursor != 1 {
			t.Fatal("crossed page advanced cursor")
		}
		page := operationFixture(ops.Preflight, args.AfterCursor, 1)
		page.Operation.ID = "op-direct"
		if calls == 2 {
			crossDirectPage(&page, crossed)
		}
		return reviewWire(t, req.Op, page)
	})
	m := directOperationModel(t, client)
	before := *m.observe
	before.Events = append([]ops.Event(nil), before.Events...)
	m, _ = press(m, "r")
	if m.observe.Err == nil || m.observe.Err.Code() != result.TransportInvalidResponse || m.observe.Cursor != 1 || m.observe.Operation != before.Operation || !reflect.DeepEqual(m.observe.Events, before.Events) || m.observe.Flags != lifecycleSelection {
		t.Fatalf("crossed identity replaced binding: %+v", m.observe)
	}
	m.flags = operationFlags{target: "elsewhere", configDir: "/elsewhere"}
	if text := m.exitNotice(); strings.Contains(text, "elsewhere") || !strings.Contains(text, lifecycleSelection.command("status", "--operation", "op-direct", "--after-cursor", "1")) {
		t.Fatal(text)
	}
	m, _ = press(m, "r")
	if m.observe.Cursor != 2 || m.observe.Err != nil || m.observe.Operation.App != before.Operation.App {
		t.Fatalf("refresh lost original identity: %+v", m.observe)
	}
	if _, quit := press(m, "ctrl+c"); !quit {
		t.Fatal("cannot exit paused direct entry")
	}
}

func crossDirectPage(page *jobs.Status, field string) {
	switch field {
	case "app":
		page.Operation.App = "different-app"
	case "plan":
		page.Operation.PlanID = fixtureDigest("b")
	case "kind":
		page.Operation.Kind, page.Operation.RecoveryOf = ops.Resolve, "op-parent"
	}
}

func TestTUIOperationEntryUndrainedTerminalNotice(t *testing.T) {
	for _, ending := range []string{"quit", "eof"} {
		t.Run(ending, func(t *testing.T) { testDirectTerminalRunner(t, ending) })
	}
	t.Run("failed continuation", testDirectTerminalFailure)
}

func testDirectTerminalFailure(t *testing.T) {
	calls := 0
	client := callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		calls++
		if calls > 1 {
			return result.Envelope{}, result.New(result.TransportInvalidResponse, nil)
		}
		page := operationFixture(ops.Failed, 0, jobs.EventPageLimit)
		page.Operation.ID = "op-direct"
		return reviewWire(t, req.Op, page)
	})
	m := directOperationModel(t, client)
	if m.observe.Cursor != jobs.EventPageLimit || m.observe.Phase != observePaused {
		t.Fatalf("lost full terminal page: %+v", m.observe)
	}
	requireUndrainedNotice(t, m.exitNotice())
	m.flags = operationFlags{target: "elsewhere"}
	requireUndrainedNotice(t, m.exitNotice())
}

func testDirectTerminalRunner(t *testing.T, ending string) {
	t.Helper()
	input, keys := pipe(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, canceled := make(chan string, 1), make(chan string, 1)
	output := &lockedBuffer{}
	var diagnostics bytes.Buffer
	client := callFunc(func(ctx context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "operation" {
			t.Errorf("direct entry requested %s", req.Op)
			return result.Envelope{}, result.New(result.InternalError, nil)
		}
		var args dispatch.OperationArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return result.Envelope{}, err
		}
		if args.AfterCursor == 0 {
			page := operationFixture(ops.Failed, 0, jobs.EventPageLimit)
			page.Operation.ID = "op-direct"
			return reviewWire(t, req.Op, page)
		}
		started <- "continuation"
		<-ctx.Done()
		canceled <- "continuation"
		return result.Envelope{}, ctx.Err()
	})
	deps := wireDependencies(t, client)
	deps.Context, deps.Stdin, deps.Stdout, deps.Stderr = ctx, input, output, &diagnostics
	withTestTerminal(t, &deps)
	deps.RunTUI = func(ctx context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		return RunTUI(ctx, model, input, output)
	}
	done := startEntryCommand(t, deps, []string{"tui", "--operation", "op-direct", "--target", "fixture", "--config-dir", lifecycleSelection.configDir}, cancel)
	receive(t, started, "continuation")
	if ending == "eof" {
		if err := keys.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		writeKeys(t, keys, "\x03")
	}
	err := awaitEntryCommand(t, done)
	receive(t, canceled, "continuation")
	if ending == "eof" {
		requireIOFailure(t, err, io.EOF)
	} else if err != nil {
		t.Fatal(err)
	}
	requireUndrainedNotice(t, diagnostics.String())
	if input.Fd() == ^uintptr(0) {
		t.Fatal("runner closed borrowed input")
	}
}

func requireUndrainedNotice(t *testing.T, notice string) {
	t.Helper()
	for _, want := range []string{"terminal", "history has not been fully observed", lifecycleSelection.command("status", "--operation", "op-direct", "--after-cursor", "128"), lifecycleSelection.command("tui", "--operation", "op-direct")} {
		if !strings.Contains(notice, want) {
			t.Fatalf("missing %q: %s", want, notice)
		}
	}
	if strings.Contains(notice, "still running") {
		t.Fatalf("terminal job called running: %s", notice)
	}
}

func awaitEntryCommand(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("entry command did not end")
	}
	return nil
}

func startEntryCommand(t *testing.T, deps Dependencies, args []string, cancel context.CancelFunc) <-chan error {
	t.Helper()
	done, finished := make(chan error, 1), make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("entry command cleanup did not join")
		}
	})
	go func() {
		defer close(finished)
		done <- Execute(deps, args)
	}()
	return done
}

func testDirectOperationRunner(t *testing.T) {
	for _, state := range []ops.State{ops.Queued, ops.Failed} {
		t.Run(string(state), func(t *testing.T) { runDirectOperationEntry(t, state) })
	}
}

func runDirectOperationEntry(t *testing.T, state ops.State) {
	t.Helper()
	input, keys := pipe(t)
	output := &lockedBuffer{}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := callFunc(func(_ context.Context, _ transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "operation" {
			t.Errorf("direct entry requested %s", req.Op)
			return result.Envelope{}, result.New(result.InternalError, nil)
		}
		page := operationFixture(state, 0, 0)
		page.Operation.ID, page.Operation.App = "op-direct", "uncommitted"
		return reviewWire(t, req.Op, page)
	})
	deps := wireDependencies(t, client)
	deps.Context, deps.Stdin, deps.Stdout = ctx, input, output
	withTestTerminal(t, &deps)
	deps.RunTUI = func(ctx context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		return RunTUI(ctx, model, input, output)
	}
	done := startEntryCommand(t, deps, []string{"tui", "--operation", "op-direct", "--target", "fixture", "--config-dir", lifecycleSelection.configDir}, cancel)
	waitForOutput(t, output, string(state))
	writeKeys(t, keys, "\x03")
	if err := awaitEntryCommand(t, done); err != nil {
		t.Fatal(err)
	}
}
