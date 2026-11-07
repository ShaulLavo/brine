package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/result"
	"golang.org/x/sys/unix"
)

func TestInitRunnerApproval(t *testing.T) {
	for _, ending := range []string{"approve", "cancel", "back", "signal", "context", "eof", "read error", "write error", "short write", "tiny", "approved runner failure"} {
		t.Run(ending, func(t *testing.T) { checkInitRunner(t, ending) })
	}
	t.Run("terminal restored", checkInitPTYApproval)
	t.Run("goldens", checkInitGoldens)
	t.Run("layouts", checkInitLayouts)
}

func checkInitRunner(t *testing.T, ending string) {
	t.Helper()
	dir := t.TempDir()
	input, keys := pipe(t)
	guardedInput := &applyInput{File: input}
	output := &applyOutput{short: ending == "short write"}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(outputText(output))
		}
	})
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	deps := initNoHostDependencies(t, output, &diagnostics)
	deps.Context, deps.Stdin = ctx, input
	withTestTerminal(t, &deps)
	deps.RunTUI = func(ctx context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		if ending == "tiny" {
			m := model.(initModel)
			m.width, m.height = 1, 1
			model = initTinyModel{m}
		}
		final, err := RunTUI(ctx, model, guardedInput, output)
		if tiny, ok := final.(initTinyModel); ok {
			final = tiny.initModel
		}
		if ending == "approved runner failure" {
			return final, errors.Join(err, io.ErrClosedPipe)
		}
		return final, err
	}
	done := make(chan error, 1)
	finished := false
	t.Cleanup(func() { cleanupInitRunner(t, cancel, keys, done, finished) })
	go func() { done <- Execute(deps, initArgs(dir)) }()
	if ending == "tiny" {
		endInitTiny(t, keys)
	} else {
		waitForOutput(t, &output.output, "a create")
		endInitRunner(t, ending, keys, guardedInput, output, cancel)
	}
	err := awaitInitCommand(t, done)
	finished = true
	checkInitRunnerResult(t, dir, ending, err)
	if input.Fd() == ^uintptr(0) {
		t.Fatal("borrowed input closed")
	}
	if ending == "cancel" || ending == "back" || ending == "tiny" {
		if !strings.Contains(outputText(output), "cancelled") {
			t.Fatalf("missing cancelled result: %s", outputText(output))
		}
	}
}

func outputText(output *applyOutput) string {
	output.output.mu.Lock()
	defer output.output.mu.Unlock()
	return output.output.buf.String()
}

type initTinyModel struct{ initModel }

func (m initTinyModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if _, sized := message.(tea.WindowSizeMsg); sized {
		return m, nil
	}
	next, command := m.initModel.Update(message)
	return initTinyModel{next.(initModel)}, command
}

func endInitTiny(t *testing.T, keys *os.File) {
	t.Helper()
	writeKeys(t, keys, "ay")
	// The real runner keeps the model unapproved at 1x1, then quits deliberately.
	writeKeys(t, keys, "q")
}

func endInitRunner(t *testing.T, ending string, keys *os.File, input *applyInput, output *applyOutput, cancel context.CancelFunc) {
	t.Helper()
	switch ending {
	case "approve", "approved runner failure":
		writeKeys(t, keys, "a")
		waitForOutput(t, &output.output, "Confirm: create")
		writeKeys(t, keys, "y")
	case "cancel":
		writeKeys(t, keys, "q")
	case "back":
		writeKeys(t, keys, "\x1b")
	case "signal":
		writeKeys(t, keys, "\x03")
	case "context":
		cancel()
	case "eof":
		if err := keys.Close(); err != nil {
			t.Fatal(err)
		}
	case "read error":
		input.fail.Store(true)
		writeKeys(t, keys, "a")
	default:
		output.fail.Store(true)
		writeKeys(t, keys, "a")
	}
}

func awaitInitCommand(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(6 * time.Second):
		t.Fatal("init runner did not end")
	}
	return nil
}

func checkInitRunnerResult(t *testing.T, dir, ending string, err error) {
	t.Helper()
	switch ending {
	case "approve":
		if err != nil {
			t.Fatal(err)
		}
		assertInitFiles(t, dir, []string{initFileName})
		return
	case "cancel", "back", "tiny":
		if err != nil {
			t.Fatal(err)
		}
	case "signal", "context":
		if result.ExitCode(err) != 130 {
			t.Fatalf("interrupt = %v", err)
		}
	default:
		if err == nil || result.ExitCode(err) != 1 {
			t.Fatalf("I/O failure = %v", err)
		}
	}
	assertInitFiles(t, dir, nil)
}

func checkInitPTYApproval(t *testing.T) {
	t.Helper()
	master, slave := openTestPTY(t)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	output := collectInitPTY(t, master, slave)
	var diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, slave, &diagnostics)
	deps.Context, deps.Stdin, deps.RunTUI = ctx, slave, RunTUI
	dir := t.TempDir()
	done := make(chan error, 1)
	finished := false
	t.Cleanup(func() { cleanupInitRunner(t, cancel, nil, done, finished) })
	go func() { done <- Execute(deps, initArgs(dir)) }()
	waitForOutput(t, output, "a create")
	writeKeys(t, master, "a")
	waitForOutput(t, output, "Confirm: create")
	writeKeys(t, master, "y")
	err = awaitInitCommand(t, done)
	finished = true
	if err != nil {
		t.Fatal(err, diagnostics.String())
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *after != *before {
		t.Fatal("terminal not restored", err)
	}
	if _, err := slave.WriteString("borrowed terminal\r\n"); err != nil {
		t.Fatal(err)
	}
	assertInitFiles(t, dir, []string{initFileName})
}

func collectInitPTY(t *testing.T, master, slave *os.File) *lockedBuffer {
	t.Helper()
	out := &lockedBuffer{}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(out, master); close(done) }()
	t.Cleanup(func() {
		_ = slave.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("PTY reader did not stop")
		}
		_ = master.Close()
	})
	return out
}

func cleanupInitRunner(t *testing.T, cancel context.CancelFunc, keys *os.File, done <-chan error, finished bool) {
	t.Helper()
	cancel()
	if keys != nil {
		_ = keys.Close()
	}
	if finished {
		return
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Error("init command outlived cleanup")
	}
}
