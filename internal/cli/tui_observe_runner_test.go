package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

// Real Bubble Tea and the existing runner I/O adapter run an operation read
// blocked until cancellation. The fake has no mutation method to invoke.
func TestRunTUIOperationDetachCancelsOnlyClientRead(t *testing.T) {
	for _, ending := range []string{"quit", "eof", "cancel"} {
		t.Run(ending, func(t *testing.T) { testOperationDetach(t, ending) })
	}
}

func testOperationDetach(t *testing.T, ending string) {
	t.Helper()
	input, writer := pipe(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, canceled := make(chan string, 1), make(chan string, 1)
	host := fixtureHost()
	host.apps = func(context.Context) (apps.Report, error) {
		report := fixtureReport()
		report.Apps[0].LastOperation.State = ops.Starting
		return report, nil
	}
	host.operation = func(ctx context.Context, id string, _ uint64) (jobs.Status, error) {
		started <- id
		<-ctx.Done()
		canceled <- id
		return jobs.Status{}, ctx.Err()
	}
	done := make(chan runOutcome, 1)
	go func() {
		session, stop := context.WithCancel(ctx)
		m := start(fixtureSession(session, host, "fixture"))
		m, _ = press(m, "enter")
		m, cmd := updateTUI(m, keyMsg("t"))
		m.initial = cmd
		final, err := RunTUI(session, m, input, &lockedBuffer{})
		stop()
		done <- runOutcome{final, err}
	}()
	receive(t, started, "op-01fixture")
	switch ending {
	case "quit":
		if _, err := writer.Write([]byte{3}); err != nil {
			t.Fatal(err)
		}
	case "eof":
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	case "cancel":
		cancel()
	}
	outcome := await(t, done)
	receive(t, canceled, "op-01fixture")
	if ending == "eof" {
		requireIOFailure(t, outcome.err, io.EOF)
	}
	if ending == "cancel" && !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("cancel error %v", outcome.err)
	}
	if ending == "quit" && outcome.err != nil {
		t.Fatalf("quit error %v", outcome.err)
	}
	if !slices.Equal(host.Calls(), []string{"apps", "operation op-01fixture 0"}) {
		t.Fatalf("unexpected host requests: %v", host.Calls())
	}
	notice := outcome.final.(tuiModel).exitNotice()
	if !strings.Contains(notice, "Operation op-01fixture is still running on fixture") || !strings.Contains(notice, "quitting did not stop it") {
		t.Fatalf("detach notice %q", notice)
	}
}

func TestTUICommandWritesOriginalObservationExitNoticeOnRunnerFailure(t *testing.T) {
	var output, diagnostics bytes.Buffer
	deps := testDependencies(t, &output, &diagnostics)
	withTestTerminal(t, &deps)
	deps.LoadOperationTarget = func(_ string, name string) (transport.Target, error) { return transport.Target{Name: name}, nil }
	deps.RunTUI = func(_ context.Context, model tea.Model, _ io.Reader, _ io.Writer) (tea.Model, error) {
		m := model.(tuiModel)
		m.observe = &tuiObservation{Flags: m.flags, Operation: operationFixture(ops.LaunchUnknown, 0, 0).Operation, Cursor: 128, Phase: observeUncertain}
		m.flags = operationFlags{target: "elsewhere", configDir: "/elsewhere"}
		return m, result.New(result.InternalError, io.ErrShortWrite)
	}
	err := Execute(deps, []string{"tui", "--target", "fixture", "--config-dir", "/fixture config"})
	if result.Classify(err).Code() != result.InternalError {
		t.Fatalf("error %v", err)
	}
	notice := diagnostics.String()
	if !strings.Contains(notice, "brine reconcile --dry-run --target fixture --config-dir '/fixture config'") || strings.Contains(notice, "elsewhere") || !strings.Contains(notice, "--after-cursor 128") {
		t.Fatalf("exit guidance lost: %q", notice)
	}
}
