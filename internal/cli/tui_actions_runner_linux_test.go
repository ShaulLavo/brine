package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type applyOutput struct {
	output lockedBuffer
	fail   atomic.Bool
	short  bool
}

func (w *applyOutput) Write(p []byte) (int, error) {
	if !w.fail.Load() {
		return w.output.Write(p)
	}
	if w.short {
		return max(0, len(p)-1), nil
	}
	return 0, io.ErrClosedPipe
}

type applyInput struct {
	*os.File
	fail atomic.Bool
}

func (r *applyInput) Read(p []byte) (int, error) {
	n, err := r.File.Read(p)
	if r.fail.Load() {
		return 0, io.ErrUnexpectedEOF
	}
	return n, err
}

func TestRunTUIPendingAcceptanceCleanupAndRecoveryNotice(t *testing.T) {
	for _, ending := range []string{"quit", "eof", "read error", "write error", "short write", "cancel"} {
		t.Run(ending, func(t *testing.T) { testPendingApplyRunner(t, ending) })
	}
}

func testPendingApplyRunner(t *testing.T, ending string) {
	t.Helper()
	input, keys := pipe(t)
	guardedInput := &applyInput{File: input}
	output := &applyOutput{short: ending == "short write"}
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, canceled := make(chan string, 1), make(chan string, 1)
	host := &lifecycleWireHost{t: t, action: actionRestart, final: "succeeded"}
	deps := wireDependencies(t, callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		if req.Op != "apply" {
			return host.Call(ctx, target, req)
		}
		host.requests = append(host.requests, target.Name+" "+req.Op+" "+string(req.Args))
		started <- "apply"
		<-ctx.Done()
		canceled <- "apply"
		return result.Envelope{}, ctx.Err()
	}))
	deps.Context, deps.Stdin, deps.Stdout, deps.Stderr = ctx, input, output, &diagnostics
	withTestTerminal(t, &deps)
	deps.RunTUI = func(ctx context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		m := model.(tuiModel)
		m.newKey = func() (string, error) { return "u4-runner-key", nil }
		return RunTUI(ctx, m, guardedInput, output)
	}
	done := make(chan error, 1)
	go func() {
		done <- Execute(deps, []string{"tui", "--target", "fixture", "--config-dir", lifecycleSelection.configDir})
	}()
	waitForOutput(t, &output.output, "fixture-api")
	writeKeys(t, keys, "\rp\r")
	waitForOutput(t, &output.output, "a apply")
	writeKeys(t, keys, "ay")
	receive(t, started, "apply")
	endPendingRunner(t, ending, keys, guardedInput, output, cancel)
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pending acceptance blocked terminal cleanup")
	}
	receive(t, canceled, "apply")
	checkPendingRunnerError(t, ending, err)
	notice := diagnostics.String()
	for _, want := range []string{"Acceptance of plan " + lifecyclePlanID + " is unknown", "--idempotency-key u4-runner-key --target fixture"} {
		if !strings.Contains(notice, want) {
			t.Errorf("missing %q in %q", want, notice)
		}
	}
	if !strings.Contains(notice, lifecycleSelection.command("apply", lifecyclePlanID, "--idempotency-key", "u4-runner-key")) {
		t.Fatal(notice)
	}
	if len(host.requests) != 2 || !strings.Contains(host.requests[0], " lifecycle ") || !strings.Contains(host.requests[1], " apply ") {
		t.Fatalf("unexpected requests: %v", host.requests)
	}
	if input.Fd() == ^uintptr(0) {
		t.Fatal("borrowed input closed")
	}
}

func endPendingRunner(t *testing.T, ending string, keys *os.File, input *applyInput, output *applyOutput, cancel context.CancelFunc) {
	t.Helper()
	switch ending {
	case "quit":
		writeKeys(t, keys, "\x03")
	case "cancel":
		cancel()
	case "eof":
		if err := keys.Close(); err != nil {
			t.Fatal(err)
		}
	case "read error":
		input.fail.Store(true)
		writeKeys(t, keys, "r")
	default:
		output.fail.Store(true)
		writeKeys(t, keys, "\x1b")
	}
}

func checkPendingRunnerError(t *testing.T, ending string, err error) {
	t.Helper()
	switch ending {
	case "quit":
		if err != nil {
			t.Fatal(err)
		}
	case "cancel":
		if !errors.Is(err, context.Canceled) || result.ExitCode(err) != 130 {
			t.Fatal(err)
		}
	case "eof":
		requireIOFailure(t, err, io.EOF)
	case "read error":
		requireIOFailure(t, err, io.ErrUnexpectedEOF)
	case "write error":
		requireIOFailure(t, err, io.ErrClosedPipe)
	case "short write":
		requireIOFailure(t, err, io.ErrShortWrite)
	}
}
