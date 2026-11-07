package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"golang.org/x/sys/unix"
)

type diagnosisRunnerWire struct {
	t         *testing.T
	mu        sync.Mutex
	requests  []string
	server    *dispatch.Server
	block     bool
	undrained bool
	failTail  bool
	started   chan string
	canceled  chan string
}

func newDiagnosisRunnerWire(t *testing.T) *diagnosisRunnerWire {
	t.Helper()
	return &diagnosisRunnerWire{t: t, server: diagnosisTestServer(), started: make(chan string, 8), canceled: make(chan string, 8)}
}

func (h *diagnosisRunnerWire) Call(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
	h.mu.Lock()
	h.requests = append(h.requests, target.Name+" "+req.Op+" "+string(req.Args))
	h.mu.Unlock()
	if req.Op == "operation" {
		return h.operation(ctx, req)
	}
	if req.Op != "diagnose" {
		h.t.Errorf("unexpected remote operation %s", req.Op)
		return result.Envelope{}, result.New(result.InternalError, nil)
	}
	if h.block {
		return h.blockRead(ctx, "diagnosis")
	}
	return (&diagnoseTransport{server: h.server}).Call(ctx, target, req)
}

func (h *diagnosisRunnerWire) blockRead(ctx context.Context, name string) (result.Envelope, error) {
	h.started <- name
	<-ctx.Done()
	h.canceled <- name
	return result.Envelope{}, ctx.Err()
}

func (h *diagnosisRunnerWire) operation(ctx context.Context, req dispatch.Request) (result.Envelope, error) {
	var args dispatch.OperationArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return result.Envelope{}, err
	}
	if args.AfterCursor > 0 && h.undrained {
		if h.failTail {
			return result.Envelope{}, result.New(result.TransportInvalidResponse, nil)
		}
		return h.blockRead(ctx, "continuation")
	}
	count := 0
	if h.undrained {
		count = jobs.EventPageLimit
	}
	page := operationFixture(ops.Failed, 0, count)
	page.Operation.ID, page.Operation.App = "op-direct", "demo"
	return reviewWire(h.t, req.Op, page)
}

func (h *diagnosisRunnerWire) assertReads(t *testing.T, diagnostic bool) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	want := []string{`fixture operation {"operation_id":"op-direct","after_cursor":0}`}
	if h.undrained {
		want = append(want, `fixture operation {"operation_id":"op-direct","after_cursor":128}`)
	}
	if diagnostic {
		want = append(want, `fixture diagnose {"app":"demo"}`)
	}
	if strings.Join(h.requests, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected scope or suggestion executed: %q, want %q", h.requests, want)
	}
}

func diagnosisRunnerDeps(t *testing.T, ctx context.Context, h OperationClient, input *os.File, read io.Reader, output io.Writer, diagnostics io.Writer) Dependencies {
	t.Helper()
	deps := wireDependencies(t, h)
	deps.Context, deps.Stdin, deps.Stdout, deps.Stderr = ctx, input, output, diagnostics
	withTestTerminal(t, &deps)
	deps.RunTUI = func(ctx context.Context, m tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		return RunTUI(ctx, m, read, output)
	}
	return deps
}

func diagnosisRunnerArgs() []string {
	return []string{"tui", "--operation", "op-direct", "--target", lifecycleSelection.target, "--config-dir", lifecycleSelection.configDir}
}

func TestTUIDiagnosisExitRunner(t *testing.T) {
	for _, ending := range []string{"x", "quit", "eof", "read error", "write error", "short write", "cancel"} {
		t.Run("blocked-"+ending, func(t *testing.T) { checkBlockedDiagnosisRunner(t, ending) })
	}
	for _, history := range []string{"drained", "blocked", "failed"} {
		t.Run("ready-"+history, func(t *testing.T) { checkReadyDiagnosisRunner(t, history) })
	}
	t.Run("PTY restoration and borrowed descriptors", testDiagnosisPTYExit)
	t.Run("last report after failed refresh", testDiagnosisRunnerLastReport)
}

func checkBlockedDiagnosisRunner(t *testing.T, ending string) {
	t.Helper()
	input, keys := pipe(t)
	guardedInput := &applyInput{File: input}
	output := &applyOutput{short: ending == "short write"}
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := newDiagnosisRunnerWire(t)
	h.block, h.undrained = true, true
	deps := diagnosisRunnerDeps(t, ctx, h, input, guardedInput, output, &diagnostics)
	done := startEntryCommand(t, deps, diagnosisRunnerArgs(), cancel)
	receive(t, h.started, "continuation")
	writeKeys(t, keys, "d")
	receive(t, h.canceled, "continuation")
	receive(t, h.started, "diagnosis")
	if ending == "x" {
		writeKeys(t, keys, "x")
	} else {
		endDiagnosisRunner(t, ending, keys, guardedInput, output, cancel)
	}
	err := awaitEntryCommand(t, done)
	receive(t, h.canceled, "diagnosis")
	checkDiagnosisRunnerError(t, ending, err)
	notice := diagnostics.String()
	requireDiagnosisUndrainedOrder(t, notice)
	if ending == "x" {
		requireDiagnosisExit(t, notice, "Diagnosis was not obtained.")
	} else if strings.Contains(notice, "Diagnosis for") || strings.Contains(notice, "Full report:") {
		t.Fatal("non-explicit ending printed diagnostic summary", notice)
	}
	h.assertReads(t, true)
	if input.Fd() == ^uintptr(0) {
		t.Fatal("borrowed input was closed")
	}
}

func endDiagnosisRunner(t *testing.T, ending string, keys *os.File, input *applyInput, output *applyOutput, cancel context.CancelFunc) {
	t.Helper()
	if ending == "write error" || ending == "short write" {
		output.fail.Store(true)
		writeKeys(t, keys, "\x03")
		return
	}
	endPendingRunner(t, ending, keys, input, output, cancel)
}

func checkDiagnosisRunnerError(t *testing.T, ending string, err error) {
	t.Helper()
	if ending == "x" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	checkPendingRunnerError(t, ending, err)
}

func checkReadyDiagnosisRunner(t *testing.T, history string) {
	t.Helper()
	input, keys := pipe(t)
	output := &lockedBuffer{}
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := newDiagnosisRunnerWire(t)
	h.undrained, h.failTail = history != "drained", history == "failed"
	deps := diagnosisRunnerDeps(t, ctx, h, input, input, output, &diagnostics)
	done := startEntryCommand(t, deps, diagnosisRunnerArgs(), cancel)
	waitDiagnosisOperation(t, output, h, history)
	writeKeys(t, keys, "d")
	if history == "blocked" {
		receive(t, h.canceled, "continuation")
	}
	waitForOutput(t, output, "Diagnosis for target fixture")
	writeKeys(t, keys, "x")
	if err := awaitEntryCommand(t, done); err != nil || result.ExitCode(err) != 0 {
		t.Fatalf("successful collection/quit became deployment result: %v", err)
	}
	notice := diagnostics.String()
	requireDiagnosisExit(t, notice, "Findings: disk_below_minimum")
	if history == "drained" {
		if strings.Contains(notice, "Observe with:") {
			t.Fatal("drained terminal history needs no recovery", notice)
		}
	} else {
		requireDiagnosisUndrainedOrder(t, notice)
	}
	h.assertReads(t, true)
}

func waitDiagnosisOperation(t *testing.T, output *lockedBuffer, h *diagnosisRunnerWire, history string) {
	t.Helper()
	switch history {
	case "blocked":
		receive(t, h.started, "continuation")
	case "failed":
		waitForOutput(t, output, "transport_invalid_response")
	default:
		waitForOutput(t, output, "settled")
	}
}

func requireDiagnosisExit(t *testing.T, notice, status string) {
	t.Helper()
	command := lifecycleSelection.command("diagnose", "demo")
	if !strings.Contains(notice, status) || !strings.HasSuffix(notice, "Full report: "+command+"\n") {
		t.Fatal("missing summary or exact quoted original command", notice)
	}
	index := strings.Index(notice, "Diagnosis for")
	if recovery := strings.Index(notice, "Observe with:"); recovery >= 0 && recovery >= index {
		t.Fatal("diagnosis overwrote or preceded recovery", notice)
	}
	checkRedacted(t, notice, true)
	if strings.Contains(notice, "password=") || strings.Contains(notice, "PLANTED_PRIVATE_CAUSE") {
		t.Fatal("exit included logs or raw cause", notice)
	}
}

func testDiagnosisPTYExit(t *testing.T) {
	master, slave := openTestPTY(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := newDiagnosisRunnerWire(t)
	collected, joined := collectDiagnosisPTY(t, master)
	notice := diagnosisRestoredNotice{t: t, terminal: slave, before: before}
	deps := diagnosisRunnerDeps(t, ctx, h, slave, slave, slave, notice)
	done := startEntryCommand(t, deps, diagnosisRunnerArgs(), cancel)
	waitForOutput(t, collected, "settled")
	writeKeys(t, master, "d")
	waitForOutput(t, collected, "Diagnosis for target fixture")
	writeKeys(t, master, "x")
	if err := awaitEntryCommand(t, done); err != nil {
		t.Fatal(err)
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *before != *after {
		t.Fatal("diagnostic exit did not restore terminal", err)
	}
	waitForOutput(t, collected, "Full report:")
	if _, err := slave.WriteString("still borrowed\r\n"); err != nil {
		t.Fatal("borrowed output was closed", err)
	}
	if err := master.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY collector did not stop")
	}
}

func collectDiagnosisPTY(t *testing.T, master *os.File) (*lockedBuffer, <-chan struct{}) {
	t.Helper()
	output, joined := &lockedBuffer{}, make(chan struct{})
	t.Cleanup(func() {
		_ = master.Close()
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("PTY collector cleanup did not join")
		}
	})
	go func() {
		defer close(joined)
		_, _ = io.Copy(output, master)
	}()
	return output, joined
}

func testDiagnosisRunnerLastReport(t *testing.T) {
	input, keys := pipe(t)
	output := &lockedBuffer{}
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	h := newDiagnosisRunnerWire(t)
	deps := diagnosisRunnerDeps(t, ctx, diagnosisRefreshFailure(h), input, input, output, &diagnostics)
	done := startEntryCommand(t, deps, diagnosisRunnerArgs(), cancel)
	waitForOutput(t, output, "settled")
	writeKeys(t, keys, "d")
	waitForOutput(t, output, "Diagnosis for target fixture")
	writeKeys(t, keys, "r")
	waitForOutput(t, output, "Last report is stale")
	writeKeys(t, keys, "x")
	if err := awaitEntryCommand(t, done); err != nil {
		t.Fatal(err)
	}
	requireDiagnosisExit(t, diagnostics.String(), "Last report is stale. Findings: disk_below_minimum")
}

func diagnosisRefreshFailure(h *diagnosisRunnerWire) OperationClient {
	reads := 0
	return callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "diagnose" {
			return h.Call(ctx, target, req)
		}
		reads++
		if reads > 1 {
			return result.Envelope{}, result.New(result.TransportInvalidResponse, nil)
		}
		return h.Call(ctx, target, req)
	})
}

func requireDiagnosisUndrainedOrder(t *testing.T, notice string) {
	t.Helper()
	requireUndrainedNotice(t, notice)
	want := "Operation op-direct on fixture is terminal; its history has not been fully observed. Observe with: " + lifecycleSelection.command("status", "--operation", "op-direct", "--after-cursor", "128") + " Resume in the TUI with: " + lifecycleSelection.command("tui", "--operation", "op-direct")
	first, _, _ := strings.Cut(notice, "\n")
	if first != want {
		t.Fatalf("first notice changed or diagnostic notice came first: %q", notice)
	}
}

type diagnosisRestoredNotice struct {
	t        *testing.T
	terminal *os.File
	before   *unix.Termios
}

func (w diagnosisRestoredNotice) Write(p []byte) (int, error) {
	after, err := unix.IoctlGetTermios(int(w.terminal.Fd()), unix.TCGETS)
	if err != nil {
		return 0, err
	}
	if *after != *w.before {
		w.t.Error("diagnostic notice written before terminal restoration")
	}
	return w.terminal.Write(p)
}
