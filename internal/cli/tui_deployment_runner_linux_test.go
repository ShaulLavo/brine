package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/charmbracelet/x/ansi"
)

type deploymentEngineSession struct {
	input       *applyInput
	keys        *os.File
	output      *applyOutput
	diagnostics bytes.Buffer
	cancel      context.CancelFunc
	done        <-chan error
	final       chan tuiModel
	changes     chan struct{}
}

func startDeploymentEngineSession(t *testing.T, f *deploymentEngineFixture, entry tuiEntry, client OperationClient) *deploymentEngineSession {
	t.Helper()
	input, keys := pipe(t)
	s := &deploymentEngineSession{input: &applyInput{File: input}, keys: keys, output: &applyOutput{}, final: make(chan tuiModel, 1), changes: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(f.ctx, 45*time.Second)
	s.cancel = cancel
	deps := f.dependencies()
	deps.Context, deps.Stdin, deps.Stdout, deps.Stderr = ctx, input, deploymentEngineOutput{s}, &s.diagnostics
	deps.OperationClient = client
	withTestTerminal(t, &deps)
	deps.RunTUI = s.run()
	args := []string{"tui", "--target", f.flags.target, "--config-dir", f.flags.configDir}
	if entry.Spec != "" {
		args = append(args, "--spec", entry.Spec)
	} else {
		args = append(args, "--operation", entry.Operation)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		s.output.output.mu.Lock()
		defer s.output.output.mu.Unlock()
		t.Logf("terminal output %q; diagnostics %q", s.output.output.buf.String(), s.diagnostics.String())
	})
	s.done = startEntryCommand(t, deps, args, cancel)
	return s
}

func (s *deploymentEngineSession) run() func(context.Context, tea.Model, io.Reader, io.Writer) (tea.Model, error) {
	return func(ctx context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		m := model.(tuiModel)
		m.after = deploymentEnginePoll
		final, err := RunTUI(ctx, m, s.input, deploymentEngineOutput{s})
		s.final <- final.(tuiModel)
		return final, err
	}
}

func deploymentEnginePoll(_ time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(5*time.Millisecond, func(time.Time) tea.Msg { return msg })
}

func (s *deploymentEngineSession) approve(t *testing.T) {
	t.Helper()
	s.confirm(t)
	s.wait(t, "queued")
}

func (s *deploymentEngineSession) confirm(t *testing.T) {
	t.Helper()
	s.wait(t, "a apply")
	writeKeys(t, s.keys, "a")
	s.wait(t, "y apply")
	writeKeys(t, s.keys, "y")
}

func (s *deploymentEngineSession) end(t *testing.T, ending string) tuiModel {
	t.Helper()
	endDiagnosisRunner(t, ending, s.keys, s.input, s.output, s.cancel)
	err := awaitEntryCommand(t, s.done)
	checkPendingRunnerError(t, ending, err)
	final := deploymentEngineFinal(t, s.final)
	if s.input.Fd() == ^uintptr(0) {
		t.Fatal("borrowed input closed")
	}
	return final
}

func (s *deploymentEngineSession) diagnoseExit(t *testing.T, f *deploymentEngineFixture) tuiModel {
	t.Helper()
	writeKeys(t, s.keys, "d")
	s.wait(t, "findings")
	writeKeys(t, s.keys, "x")
	if err := awaitEntryCommand(t, s.done); err != nil {
		t.Fatal(err)
	}
	final := deploymentEngineFinal(t, s.final)
	if !strings.Contains(s.diagnostics.String(), f.flags.command("diagnose", "hello")) || !strings.Contains(s.diagnostics.String(), "operation_failed") {
		t.Fatal("missing failed-release diagnosis guidance", s.diagnostics.String())
	}
	return final
}

func TestTUIDeploymentRunnerDetachResume(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", failed), func(t *testing.T) { deploymentEngineEndings(t, failed) })
	}
}

func testDeploymentEngineDetach(t *testing.T, failed bool, ending string) {
	t.Helper()
	f := newDeploymentEngineFixture(t, failed, true)
	read := &deploymentEngineReadBlock{fixture: f, started: make(chan uint64, 1), done: make(chan struct{})}
	s := startDeploymentEngineSession(t, f, tuiEntry{Spec: f.path}, read)
	s.output.short = ending == "short write"
	s.approve(t)
	id := f.operationID()
	close(f.gate)
	deploymentEngineWait(t, f.pulling, "real pull entry")
	s.wait(t, "preparing")
	cursor := deploymentEngineReceive(t, read.started)
	final := s.end(t, ending)
	deploymentEngineWait(t, read.done, "canceled client read")
	if final.observe == nil || final.observe.Cursor == 0 || final.observe.Cursor != cursor || final.observe.Operation.ID != id || final.observe.Phase == observeSettled {
		t.Fatalf("detach lost original observer: %+v", final.observe)
	}
	want := f.flags.command("status", "--operation", id, "--after-cursor", fmt.Sprint(final.observe.Cursor))
	if !strings.Contains(s.diagnostics.String(), want) || !strings.Contains(s.diagnostics.String(), f.flags.command("tui", "--operation", id)) {
		t.Fatal("detach lost original-context notices", s.diagnostics.String())
	}
	select {
	case <-f.done:
		t.Fatal("client ending killed or completed the independently gated worker")
	default:
	}
	if f.ctx.Err() != nil {
		t.Fatal("client canceled runner lifetime", f.ctx.Err())
	}
	close(f.pullGate)
	f.finish()
	snapshot := f.assertOutcome()
	if snapshot.Operation.ID != id {
		t.Fatal("runner changed operation")
	}
	resume := startDeploymentEngineSession(t, f, tuiEntry{Operation: id}, f)
	resume.wait(t, "settled")
	observed := resume.end(t, "quit")
	assertDeploymentEngineObservation(t, observed, snapshot)
	f.assertMutations(1, 1)
}

func TestDeployCommandUsesReleaseEngine(t *testing.T) {
	t.Run("success and retained checkpoints", testDeploymentEngineCombinedSuccess)
	t.Run("definite pull failure", testDeploymentEngineCombinedFailure)
	t.Run("accepted client interruption resumes in TUI", testDeploymentEngineCombinedInterruption)
}

func testDeploymentEngineCombinedSuccess(t *testing.T) {
	f := newDeploymentEngineFixture(t, false, false)
	close(f.gate)
	envelope, err := f.machine("deploy", f.path, "--yes")
	report := deploymentEngineData[deploymentReport](t, envelope, err)
	f.finish()
	f.assertReport(report, true)
	f.assertOutcome()
	f.assertMutations(1, 1)
	envelope, err = f.machine("deploy", f.path, "--yes")
	deploymentEngineData[deploymentReport](t, envelope, err)
	if f.requestCount("deploy_state") < 2 || f.requestCount("deploy_checkpoint") == 0 {
		t.Fatal("combined deploy bypassed real state/checkpoint methods")
	}
	f.assertMutations(2, 1)
}

func testDeploymentEngineCombinedFailure(t *testing.T) {
	f := newDeploymentEngineFixture(t, true, false)
	close(f.gate)
	envelope, err := f.machine("deploy", f.path, "--yes")
	if result.Classify(err).Code() != result.RecoveryRequired || result.ExitCode(err) != 6 || envelope.OK {
		t.Fatalf("failure invocation: %v %+v", err, envelope)
	}
	report := deploymentEngineFailureReport(t, envelope)
	f.finish()
	f.assertReport(report, false)
	f.assertOutcome()
	f.assertMutations(1, 1)
}

func testDeploymentEngineCombinedInterruption(t *testing.T) {
	f := newDeploymentEngineFixture(t, true, true)
	ctx, cancel := context.WithCancel(f.ctx)
	var output, diagnostics bytes.Buffer
	deps := f.dependencies()
	deps.Context, deps.Stdout, deps.Stderr = ctx, &output, &diagnostics
	args := []string{"deploy", f.path, "--yes", "--json", "--no-input", "--target", f.flags.target, "--config-dir", f.flags.configDir}
	close(f.gate)
	done := startEntryCommand(t, deps, args, cancel)
	deploymentEngineWait(t, f.pulling, "combined real pull entry")
	cancel()
	err := awaitEntryCommand(t, done)
	if result.ExitCode(err) != 130 {
		t.Fatalf("interrupted deploy: %v, output %s", err, output.String())
	}
	envelope := deploymentEngineEnvelope(t, output.Bytes())
	report := deploymentEngineFailureReport(t, envelope)
	f.assertReport(report, false)
	id := f.operationID()
	if !strings.Contains(diagnostics.String(), f.flags.command("status", "--operation", id)) {
		t.Fatal("accepted interruption lost operation", diagnostics.String())
	}
	close(f.pullGate)
	f.finish()
	status := f.assertOutcome()
	resume := startDeploymentEngineSession(t, f, tuiEntry{Operation: id}, f)
	resume.wait(t, "settled")
	final := resume.diagnoseExit(t, f)
	assertDeploymentEngineObservation(t, final, status)
	f.assertMutations(1, 1)
}

func deploymentEngineEndings(t *testing.T, failed bool) {
	for _, ending := range []string{"quit", "eof", "read error", "write error", "short write", "cancel"} {
		t.Run(ending, func(t *testing.T) { testDeploymentEngineDetach(t, failed, ending) })
	}
}

type deploymentEngineReadBlock struct {
	fixture *deploymentEngineFixture
	started chan uint64
	done    chan struct{}
	cursor  atomic.Uint64
}

func (h *deploymentEngineReadBlock) Call(ctx context.Context, selected transport.Target, req dispatch.Request) (result.Envelope, error) {
	if req.Op != "operation" {
		return h.fixture.Call(ctx, selected, req)
	}
	var args dispatch.OperationArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return result.Envelope{}, err
	}
	if args.AfterCursor != 0 && args.AfterCursor == h.cursor.Load() {
		h.started <- args.AfterCursor
		defer close(h.done)
		<-ctx.Done()
		return result.Envelope{}, ctx.Err()
	}
	response, err := h.fixture.Call(ctx, selected, req)
	status, ok := response.Data.(jobs.Status)
	if err == nil && ok && status.Operation.State == ops.Preparing {
		h.cursor.Store(status.NextCursor)
	}
	return response, err
}

func deploymentEngineReceive[T any](t *testing.T, values <-chan T) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("request was not observed")
	}
	var zero T
	return zero
}

type deploymentEngineOutput struct{ session *deploymentEngineSession }

func (w deploymentEngineOutput) Write(p []byte) (int, error) {
	n, err := w.session.output.Write(p)
	select {
	case w.session.changes <- struct{}{}:
	default:
	}
	return n, err
}

func (s *deploymentEngineSession) wait(t *testing.T, text string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		s.output.output.mu.Lock()
		found := strings.Contains(ansi.Strip(s.output.output.buf.String()), text)
		s.output.output.mu.Unlock()
		if found {
			return
		}
		select {
		case <-s.changes:
		case <-timer.C:
			t.Fatalf("terminal never showed %q", text)
		}
	}
}

func deploymentEngineFinal(t *testing.T, models <-chan tuiModel) tuiModel {
	t.Helper()
	select {
	case model := <-models:
		return model
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not return its final model")
	}
	return tuiModel{}
}
