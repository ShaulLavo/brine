package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/result"
)

// These tests run the real RunTUI: Bubble Tea, its input reader and renderer,
// with the session handling newTUICmd applies around it.

type runOutcome struct {
	final tea.Model
	err   error
}

// runSession starts the real runner as newTUICmd does: the model holds the
// session context, which is canceled as soon as the runner returns.
func runSession(ctx context.Context, host tuiHost, run func(context.Context, tea.Model) (tea.Model, error)) <-chan runOutcome {
	session, cancel := context.WithCancel(ctx)
	done := make(chan runOutcome, 1)
	go func() {
		final, err := run(session, fixtureSession(session, host, "fixture"))
		cancel()
		done <- runOutcome{final, err}
	}()
	return done
}

func await(t *testing.T, done <-chan runOutcome) runOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		if _, ok := outcome.final.(tuiModel); !ok {
			t.Fatalf("runner returned %T, not the final TUI model", outcome.final)
		}
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("RunTUI did not end")
	}
	return runOutcome{}
}

func pipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})
	return r, w
}

// lockedBuffer collects renderer output written from Bubble Tea's goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func requireIOFailure(t *testing.T, err, cause error) {
	t.Helper()
	if result.Classify(err).Code() != result.InternalError || result.ExitCode(err) != 1 || !errors.Is(err, cause) {
		t.Fatalf("error = %v (%s); want internal_error caused by %v", err, result.Classify(err).Code(), cause)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("an I/O failure must not be classified as an interrupt")
	}
}

func TestRunTUIClosedInputEndsSession(t *testing.T) {
	input, closeInput := pipe(t)
	host := newBlockingHost()
	done := runSession(context.Background(), host, func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return RunTUI(ctx, m, input, &lockedBuffer{})
	})
	receive(t, host.started, "apps")
	if err := closeInput.Close(); err != nil {
		t.Fatal(err)
	}
	outcome := await(t, done)
	requireIOFailure(t, outcome.err, io.EOF)
	receive(t, host.canceled, "apps")
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestRunTUIRejectsDescriptorFreeReader(t *testing.T) {
	gone := errors.New("terminal gone")
	done := runSession(context.Background(), fixtureHost(), func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return RunTUI(ctx, m, failingReader{gone}, &lockedBuffer{})
	})
	outcome := await(t, done)
	if result.Classify(outcome.err).Code() != result.TUITerminalRequired || errors.Is(outcome.err, gone) {
		t.Fatalf("unsupported reader was used instead of rejected: %v", outcome.err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return max(0, len(p)-1), nil }

func TestRunTUIOutputFailureEndsSession(t *testing.T) {
	broken := errors.New("output closed")
	for name, tc := range map[string]struct {
		output io.Writer
		cause  error
	}{
		"write error": {failingWriter{broken}, broken},
		"short write": {shortWriter{}, io.ErrShortWrite},
	} {
		t.Run(name, func(t *testing.T) {
			input, _ := pipe(t)
			host := newBlockingHost()
			done := runSession(context.Background(), host, func(ctx context.Context, m tea.Model) (tea.Model, error) {
				return RunTUI(ctx, m, input, tc.output)
			})
			outcome := await(t, done)
			requireIOFailure(t, outcome.err, tc.cause)
			drainCanceledRead(t, host)
		})
	}
}

// drainCanceledRead accepts a session that failed before or after its first
// read began; a read that began must have been canceled.
func drainCanceledRead(t *testing.T, host *blockingHost) {
	t.Helper()
	select {
	case <-host.started:
		receive(t, host.canceled, "apps")
	default:
	}
}

func TestRunTUIControllingTerminalOwnership(t *testing.T) {
	processStdin, _ := pipe(t) // redirected, not a terminal
	tty, hangup := pipe(t)     // stands in for the controlling terminal
	opened := 0
	terminal := tuiTerminal{stdin: processStdin, openTTY: func() (*os.File, error) {
		opened++
		return tty, nil
	}}
	host := newBlockingHost()
	done := runSession(context.Background(), host, func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return terminal.run(ctx, m, nil, &lockedBuffer{})
	})
	receive(t, host.started, "apps")
	// Closing the opened terminal ends the session, so it really was the input.
	if err := hangup.Close(); err != nil {
		t.Fatal(err)
	}
	requireIOFailure(t, await(t, done).err, io.EOF)
	receive(t, host.canceled, "apps")
	if opened != 1 {
		t.Fatalf("opened the controlling terminal %d times", opened)
	}
	if _, err := tty.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("runner left its own terminal open: %v", err)
	}
	if processStdin.Fd() == ^uintptr(0) {
		t.Fatal("runner closed the process stdin it did not open")
	}

	// Explicit input is never replaced and never closed.
	terminal.openTTY = func() (*os.File, error) {
		t.Error("explicit input fell back to the controlling terminal")
		return nil, errors.New("unexpected open")
	}
	input, closeInput := pipe(t)
	done = runSession(context.Background(), fixtureHost(), func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return terminal.run(ctx, m, input, &lockedBuffer{})
	})
	if err := closeInput.Close(); err != nil {
		t.Fatal(err)
	}
	requireIOFailure(t, await(t, done).err, io.EOF)
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("borrowed input was closed: %v", err)
	}

	// Without any terminal the runner refuses before starting the program.
	terminal.openTTY = func() (*os.File, error) { return nil, os.ErrNotExist }
	m := fixtureModel(fixtureHost(), "fixture")
	if _, err := terminal.run(context.Background(), m, nil, &lockedBuffer{}); result.Classify(err).Code() != result.TUITerminalRequired {
		t.Fatalf("error = %v", err)
	}
	m.cancelRead()
}

func TestRunTUIExternalCancellationIsInterrupt(t *testing.T) {
	input, _ := pipe(t)
	host := newBlockingHost()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runSession(ctx, host, func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return RunTUI(ctx, m, input, &lockedBuffer{})
	})
	receive(t, host.started, "apps")
	cancel()
	outcome := await(t, done)
	if result.Classify(outcome.err).Code() != result.Interrupted || result.ExitCode(outcome.err) != 130 {
		t.Fatalf("error = %v; want interrupted", outcome.err)
	}
	receive(t, host.canceled, "apps")
}

// observedPipe reports an actual blocked pipe read, without inspecting process
// stacks or depending on the model's request timing.
type observedPipe struct {
	reader  *io.PipeReader
	started chan struct{}
	ended   chan struct{}
	once    sync.Once
}

func (p *observedPipe) Read(buf []byte) (int, error) {
	p.once.Do(func() { close(p.started) })
	n, err := p.reader.Read(buf)
	close(p.ended)
	return n, err
}

func TestRunTUIRejectsUninterruptibleBorrowedInput(t *testing.T) {
	input, writer := io.Pipe()
	defer func() { _ = input.Close(); _ = writer.Close() }()
	tracked := &observedPipe{reader: input, started: make(chan struct{}), ended: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runSession(ctx, fixtureHost(), func(ctx context.Context, m tea.Model) (tea.Model, error) {
		return RunTUI(ctx, m, tracked, &lockedBuffer{})
	})
	select {
	case <-tracked.started:
		cancel()
		outcome := await(t, done)
		_ = writer.Close()
		select {
		case <-tracked.ended:
		case <-time.After(5 * time.Second):
			t.Fatal("borrowed pipe read did not drain after test cleanup")
		}
		t.Fatalf("unsupported borrowed input was read and outlived admission: %v", outcome.err)
	case outcome := <-done:
		if result.Classify(outcome.err).Code() != result.TUITerminalRequired {
			t.Fatalf("unsupported input returned %v", outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unsupported input was neither refused nor read")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("runner closed borrowed input: %v", err)
	}
}

func writeKeys(t *testing.T, output io.Writer, keys string) {
	t.Helper()
	if _, err := io.WriteString(output, keys); err != nil {
		t.Fatal(err)
	}
}

func waitForOutput(t *testing.T, out *lockedBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out.mu.Lock()
		found := strings.Contains(out.buf.String(), text)
		out.mu.Unlock()
		if found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal never showed %q", text)
}
