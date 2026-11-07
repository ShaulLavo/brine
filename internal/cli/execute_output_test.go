package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
)

type finalOutputProbe struct {
	cause error
	short bool
	calls int
	data  bytes.Buffer
}

func (w *finalOutputProbe) Write(p []byte) (int, error) {
	w.calls++
	_, _ = w.data.Write(p)
	n := len(p)
	if w.short && n > 0 {
		n--
	}
	return n, w.cause
}

func TestExecuteFinalBufferedOutput(t *testing.T) {
	for _, kind := range []string{"json", "jsonl", "help json", "help jsonl", "failure json", "failure jsonl", "empty success", "buffered human", "implicit machine"} {
		t.Run(kind, func(t *testing.T) { checkFinalOutputKind(t, kind) })
	}
}

func checkFinalOutputKind(t *testing.T, kind string) {
	t.Helper()
	for _, outcome := range []string{"short", "short and error", "error", "complete"} {
		t.Run(outcome, func(t *testing.T) { checkFinalOutput(t, kind, outcome) })
	}
}

func checkFinalOutput(t *testing.T, kind, outcome string) {
	t.Helper()
	writer := &finalOutputProbe{short: strings.HasPrefix(outcome, "short")}
	if strings.Contains(outcome, "error") {
		writer.cause = io.ErrClosedPipe
	}
	var diagnostics bytes.Buffer
	deps := testDependencies(t, writer, &diagnostics)
	args := finalOutputArguments(t, &deps, kind)
	err := Execute(deps, args)
	if writer.calls != 1 || writer.data.Len() == 0 {
		t.Fatalf("writes = %d, bytes = %d", writer.calls, writer.data.Len())
	}
	if outcome == "complete" {
		checkCompleteFinalOutput(t, kind, writer.data.Bytes(), err)
		return
	}
	cause := writer.cause
	if cause == nil {
		cause = io.ErrShortWrite
	}
	if err == nil || result.Classify(err).Code() != result.InternalError || result.ExitCode(err) != 1 || !errors.Is(err, cause) {
		t.Fatalf("final output = %v; want %v", err, cause)
	}
	if writer.cause != nil && errors.Is(err, io.ErrShortWrite) {
		t.Fatal("replaced the underlying writer error")
	}
}

func finalOutputArguments(t *testing.T, deps *Dependencies, kind string) []string {
	t.Helper()
	switch kind {
	case "json":
		return []string{"version", "--json"}
	case "jsonl":
		deps.LogsClient = &logsCaller{lines: []logs.Line{{Timestamp: "2026-11-07T00:00:00Z", Priority: 6, Message: "ready"}}}
		return []string{"logs", "hello", "--target", "fixture", "--config-dir", targetConfig(t), "--jsonl"}
	case "help json":
		return []string{"version", "--help", "--json"}
	case "help jsonl":
		return []string{"version", "--help", "--jsonl"}
	case "failure json":
		return []string{"not-a-command", "--json"}
	case "failure jsonl":
		return []string{"not-a-command", "--jsonl"}
	case "empty success":
		deps.HostEngineExec = func(context.Context, string) error { return nil }
		return []string{"host", "engine-exec", strings.Repeat("a", 32), "--json"}
	default:
		deps.HostEngineCopyObserve = func(_ context.Context, _ io.Reader, output io.Writer) error { return finalOutputRuntime(output, kind) }
		return []string{"host", "engine-copy-observe"}
	}
}

func finalOutputRuntime(output io.Writer, kind string) error {
	if kind == "implicit machine" {
		return json.NewEncoder(output).Encode(result.Success("brine host engine-copy-observe", map[string]string{"status": "fixture"}))
	}
	_, err := io.WriteString(output, "fixture runtime output\n")
	return err
}

func checkCompleteFinalOutput(t *testing.T, kind string, data []byte, err error) {
	t.Helper()
	if strings.HasPrefix(kind, "failure") {
		if err == nil || result.Classify(err).Code() != result.InvalidUsage {
			t.Fatalf("lost original invocation error %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if kind == "jsonl" {
		if json.Valid(data) || !validEnvelopeStream(data, "brine logs") {
			t.Fatalf("not a multi-envelope JSONL stream: %s", data)
		}
		return
	}
	if kind != "buffered human" && !json.Valid(data) {
		t.Fatalf("invalid final JSON: %s", data)
	}
}
