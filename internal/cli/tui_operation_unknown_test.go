package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestTUIUnknownOperationDoesNotClaimAcceptance(t *testing.T) {
	for _, mode := range []string{"blocked", "malformed"} {
		t.Run(mode, func(t *testing.T) { checkTUIUnknownOperation(t, mode) })
	}
}

func checkTUIUnknownOperation(t *testing.T, mode string) {
	t.Helper()
	input, keys := pipe(t)
	output := &lockedBuffer{}
	var diagnostics bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, canceled := make(chan string, 1), make(chan string, 1)
	var calls atomic.Int32
	client := callFunc(func(ctx context.Context, target transport.Target, req dispatch.Request) (result.Envelope, error) {
		calls.Add(1)
		checkTUIUnknownScope(t, target, req, 0)
		started <- "read"
		if mode == "malformed" {
			return dispatch.DecodeResponse([]byte(`{"schema_version":1,"command":"brine host operation","ok":true,"data":{},"error":null}`), "operation")
		}
		<-ctx.Done()
		canceled <- "read"
		return result.Envelope{}, ctx.Err()
	})
	deps := wireDependencies(t, client)
	deps.Context, deps.Stdin, deps.Stdout, deps.Stderr = ctx, input, output, &diagnostics
	withTestTerminal(t, &deps)
	var final tuiModel
	deps.RunTUI = func(ctx context.Context, m tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		f, err := RunTUI(ctx, m, input, output)
		final = f.(tuiModel)
		return f, err
	}
	done := startEntryCommand(t, deps, []string{"tui", "--operation", "op-direct", "--target", "fixture", "--config-dir", lifecycleSelection.configDir}, cancel)
	receive(t, started, "read")
	marker := "op-direct"
	if mode == "malformed" {
		marker = "transport_invalid_response"
	}
	waitForOutput(t, output, marker)
	writeKeys(t, keys, "\x03")
	if err := awaitEntryCommand(t, done); err != nil {
		t.Fatal(err)
	}
	if mode == "blocked" {
		receive(t, canceled, "read")
	}
	if final.observe.Bound || final.observe.Cursor != 0 || calls.Load() != 1 {
		t.Fatalf("unexpected validated page or request: %+v, calls %d", final.observe, calls.Load())
	}
	output.mu.Lock()
	screen := output.buf.String()
	output.mu.Unlock()
	if strings.Contains(screen, "Accepted.") || strings.Contains(diagnostics.String(), "still running") {
		t.Errorf("unvalidated direct ID claims acceptance/running; Bound=false, state=%q; exit=%q", final.observe.Operation.State, diagnostics.String())
	}
	for _, expected := range []string{"Operation state was not obtained", "--after-cursor 0", "--operation op-direct", "--target fixture", lifecycleSelection.command("tui", "--operation", "op-direct")} {
		if !strings.Contains(diagnostics.String(), expected) {
			t.Errorf("missing recovery %q in %q", expected, diagnostics.String())
		}
	}
}

func checkTUIUnknownScope(t *testing.T, target transport.Target, req dispatch.Request, cursor uint64) {
	t.Helper()
	var args dispatch.OperationArgs
	if req.Op != "operation" || target.Name != "fixture" || json.Unmarshal(req.Args, &args) != nil || args.OperationID != "op-direct" || args.AfterCursor != cursor {
		t.Errorf("crossed direct request: %s %s %s, want cursor %d", target.Name, req.Op, req.Args, cursor)
	}
}
