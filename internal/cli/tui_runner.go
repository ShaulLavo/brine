package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/result"
)

// RunTUI runs model on the terminal until it quits, ctx ends, or terminal I/O
// fails. A nil stdin uses the process's stdin when it is a terminal and
// otherwise opens the controlling terminal, which RunTUI closes before
// returning. Explicit input is used as given and never replaced.
//
// The pinned Bubble Tea ends neither on input EOF nor on output write errors,
// so RunTUI watches both and ends the program on the first failure. That
// failure is returned as internal_error; ctx cancellation stays an interrupt.
func RunTUI(ctx context.Context, model tea.Model, stdin io.Reader, stdout io.Writer) (tea.Model, error) {
	return tuiTerminal{stdin: os.Stdin, openTTY: openControllingTerminal}.run(ctx, model, stdin, stdout)
}

func openControllingTerminal() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

type tuiTerminal struct {
	stdin   *os.File
	openTTY func() (*os.File, error)
}

func (t tuiTerminal) run(ctx context.Context, model tea.Model, stdin io.Reader, stdout io.Writer) (tea.Model, error) {
	input, owned, err := t.input(stdin)
	if err != nil {
		return model, result.New(result.TUITerminalRequired, err)
	}
	if owned != nil {
		defer func() { _ = owned.Close() }()
	}
	guard := &tuiIO{}
	program := tea.NewProgram(model,
		tea.WithInput(guard.reader(input)),
		tea.WithOutput(guard.writer(stdout)),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(80, 24))
	guard.quit = program.Quit
	// Every ending is a graceful quit. Bubble Tea's context-kill path closes its
	// cancelable reader without waiting for the read loop, which races with that
	// loop and outlives the runner.
	stopInterrupt := context.AfterFunc(ctx, func() { guard.end(ctx.Err()) })
	final, err := program.Run()
	stopInterrupt()
	var failure *tuiIOError
	switch cause := guard.cause(); {
	case errors.As(cause, &failure):
		return final, result.New(result.InternalError, failure)
	case cause != nil:
		return final, cause
	}
	return final, err
}

// input resolves the reader once, so the runner, not Bubble Tea, owns any
// terminal it opens.
func (t tuiTerminal) input(stdin io.Reader) (io.Reader, *os.File, error) {
	if stdin != nil {
		return stdin, nil, nil
	}
	if isTerminal(t.stdin) {
		return t.stdin, nil, nil
	}
	tty, err := t.openTTY()
	if err != nil {
		return nil, nil, err
	}
	return tty, tty, nil
}

// tuiIOError is the first terminal read or write failure of a TUI session.
type tuiIOError struct {
	op  string
	err error
}

func (e *tuiIOError) Error() string { return "terminal " + e.op + ": " + e.err.Error() }
func (e *tuiIOError) Unwrap() error { return e.err }

// tuiIO ends the session on the first failed or short read or write, or on
// interruption, and keeps whichever came first. Bubble Tea shuts its reader
// down by cancellation, so a read error here is real.
type tuiIO struct {
	mu    sync.Mutex
	first error
	once  sync.Once
	quit  func() // set before the program runs
}

// end records the first cause and asks the program to quit once. The request
// is asynchronous: a write runs inside the renderer's lock, and a synchronous
// quit would wait on an event loop that is waiting for that lock.
func (g *tuiIO) end(cause error) {
	g.mu.Lock()
	if g.first == nil {
		g.first = cause
	}
	g.mu.Unlock()
	g.once.Do(func() { go g.quit() })
}

func (g *tuiIO) cause() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.first
}

func (g *tuiIO) read(r io.Reader, p []byte) (int, error) {
	n, err := r.Read(p)
	if err != nil {
		g.end(&tuiIOError{op: "read", err: err})
	}
	return n, err
}

func (g *tuiIO) write(w io.Writer, p []byte) (int, error) {
	n, err := w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		g.end(&tuiIOError{op: "write", err: err})
	}
	return n, err
}

// reader and writer keep an *os.File's descriptor and name visible, so Bubble
// Tea still enters raw mode, reads the window size and cancels a blocked read
// through epoll or kqueue instead of leaving a reader goroutine behind.
func (g *tuiIO) reader(r io.Reader) io.Reader {
	if file, ok := r.(*os.File); ok {
		return tuiFile{file: file, io: g}
	}
	return tuiReader{r: r, io: g}
}

func (g *tuiIO) writer(w io.Writer) io.Writer {
	if file, ok := w.(*os.File); ok {
		return tuiFile{file: file, io: g}
	}
	return tuiWriter{w: w, io: g}
}

// tuiFile borrows a terminal file: it exposes only what Bubble Tea and its
// cancelable reader need, routes all I/O through the guard, and never closes
// the file.
type tuiFile struct {
	file *os.File
	io   *tuiIO
}

func (f tuiFile) Read(p []byte) (int, error)  { return f.io.read(f.file, p) }
func (f tuiFile) Write(p []byte) (int, error) { return f.io.write(f.file, p) }
func (tuiFile) Close() error                  { return nil }
func (f tuiFile) Fd() uintptr                 { return f.file.Fd() }
func (f tuiFile) Name() string                { return f.file.Name() }

type tuiReader struct {
	r  io.Reader
	io *tuiIO
}

func (r tuiReader) Read(p []byte) (int, error) { return r.io.read(r.r, p) }

type tuiWriter struct {
	w  io.Writer
	io *tuiIO
}

func (w tuiWriter) Write(p []byte) (int, error) { return w.io.write(w.w, p) }
