package logs

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
)

const containerStdout = "2026-10-09T10:33:28.366482000Z ready\n2026-10-09T10:33:30.000000001Z API_KEY=planted-secret\n"
const containerStderr = "2026-10-09T10:33:29.000000000Z failed request\n"

type containerExecutor struct {
	journal  localexec.Result
	commands []localexec.Command
}

func (e *containerExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	e.commands = append(e.commands, c)
	if c.Path == "journalctl" {
		return e.journal, &localexec.Error{Kind: localexec.Failed, ExitCode: e.journal.ExitCode}
	}
	if c.Path == "podman" && len(c.Args) > 1 && c.Args[1] == "inspect" {
		return localexec.Result{Stdout: `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","unit":"api.service","driver":"k8s-file"}`}, nil
	}
	return localexec.Result{Stdout: containerStdout, Stderr: containerStderr}, nil
}

func volatileJournal(t *testing.T) localexec.Result {
	t.Helper()
	raw, err := os.ReadFile("testdata/debian13-volatile-journal.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return localexec.Result{Stdout: fixture.Stdout, Stderr: fixture.Stderr, ExitCode: fixture.ExitCode}
}

func TestAppLogsDoNotRequireJournalAccess(t *testing.T) {
	e := &containerExecutor{journal: volatileJournal(t)}
	lines, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5, Since: "2026-10-01T00:00:00Z"})
	if err != nil || len(lines) != 3 {
		t.Fatalf("app logs unavailable with volatile journal: lines=%v error=%v", lines, err)
	}
	if lines[0].Message != "ready" || lines[1].Message != "failed request" || lines[1].Priority != 3 || lines[2].Message != "API_KEY=[REDACTED]" {
		t.Fatalf("lost, misordered or unredacted streams: %+v", lines)
	}
	if len(e.commands) != 2 {
		t.Fatalf("unexpected collection steps: %+v", e.commands)
	}
	want := []string{"--remote=false", "logs", "--timestamps", "--tail", "5", "--since", "2026-10-01T00:00:00Z", strings.Repeat("a", 64)}
	if c := e.commands[1]; c.Path != "podman" || !reflect.DeepEqual(c.Args, want) || c.Timeout != ReadTimeout || c.Mutation {
		t.Fatalf("unsafe container log command: %+v", c)
	}
	for _, c := range e.commands {
		if c.Path == "journalctl" || strings.Contains(strings.Join(c.Args, " "), "--follow") {
			t.Fatalf("app logs depended on journald or an unbounded stream: %+v", c)
		}
	}
}

func TestUnreadableUnitJournalIsUnavailable(t *testing.T) {
	e := &containerExecutor{journal: volatileJournal(t)}
	lines, err := (JournalReader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5})
	if err == nil || lines != nil || result.Classify(err).Code() != result.LogsJournalUnavailable || len(e.commands) != 1 || e.commands[0].Path != "journalctl" {
		t.Fatalf("lines=%v error=%v commands=%+v", lines, err, e.commands)
	}
	if strings.Contains(err.Error(), e.journal.Stderr) {
		t.Fatal("raw journal stderr leaked")
	}
}

type scriptedContainer struct {
	inspect    localexec.Result
	logs       localexec.Result
	inspectErr error
	logsErr    error
	commands   []localexec.Command
}

func (e *scriptedContainer) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	e.commands = append(e.commands, c)
	if len(e.commands) == 1 {
		return e.inspect, e.inspectErr
	}
	return e.logs, e.logsErr
}

func validContainer() localexec.Result {
	return localexec.Result{Stdout: `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","unit":"api.service","driver":"k8s-file"}`}
}

func TestContainerOwnershipAndDriverRefusals(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		code result.Code
	}{
		{`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"foreign","unit":"api.service","driver":"k8s-file"}`, result.LogsOwnershipRefused},
		{`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","unit":"foreign.service","driver":"k8s-file"}`, result.LogsOwnershipRefused},
		{`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","unit":null,"driver":"k8s-file"}`, result.LogsOwnershipRefused},
		{`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","name":"foreign","unit":"api.service","driver":"k8s-file"}`, result.LogsOwnershipRefused},
		{`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","unit":"api.service","driver":"journald"}`, result.LogsContainerUnavailable},
		{`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"systemd-api","unit":"api.service","driver":null}`, result.LogsContainerUnavailable},
	} {
		e := &scriptedContainer{inspect: localexec.Result{Stdout: tt.raw}}
		lines, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5})
		if lines != nil || err == nil || result.Classify(err).Code() != tt.code || len(e.commands) != 1 {
			t.Fatalf("lines=%v error=%v commands=%+v", lines, err, e.commands)
		}
	}
}

func TestContainerFailuresNeverPublishPartialOutput(t *testing.T) {
	for _, step := range []string{"inspect", "logs"} {
		for _, tt := range []struct {
			err  error
			code result.Code
		}{
			{&localexec.Error{Kind: localexec.Failed, ExitCode: 1}, result.LogsContainerFailed},
			{&localexec.Error{Kind: localexec.Timeout}, result.LogsContainerTimeout},
			{&localexec.Error{Kind: localexec.NotFound}, result.LogsContainerUnavailable},
			{context.DeadlineExceeded, result.LogsContainerTimeout},
			{context.Canceled, result.Interrupted},
		} {
			e := &scriptedContainer{inspect: validContainer(), logs: localexec.Result{Stdout: containerStdout, Stderr: "Bearer planted-secret"}}
			if step == "inspect" {
				e.inspectErr = tt.err
			} else {
				e.logsErr = tt.err
			}
			lines, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5})
			if lines != nil || err == nil || result.Classify(err).Code() != tt.code || strings.Contains(err.Error(), "planted-secret") {
				t.Fatalf("step=%s lines=%v error=%v", step, lines, err)
			}
		}
	}
}

func TestContainerParsingBounds(t *testing.T) {
	for _, tt := range []struct {
		out  localexec.Result
		code result.Code
	}{
		{localexec.Result{Stdout: strings.Repeat("x", MaxBytes/2+1), Stderr: strings.Repeat("y", MaxBytes/2+1)}, result.LogsLimitExceeded},
		{localexec.Result{Stdout: containerStdout, Stderr: containerStderr}, result.LogsLimitExceeded},
		{localexec.Result{Stdout: "without timestamp\n"}, result.LogsInvalidContainer},
		{localexec.Result{Stdout: "2026-99-09T10:33:28.366482000Z private\n"}, result.LogsInvalidContainer},
		{localexec.Result{Stdout: "2026-10-09T10:33:28.366482000Z ready\n\n"}, result.LogsInvalidContainer},
		{localexec.Result{Stderr: "Error: password=planted-secret\n"}, result.LogsInvalidContainer},
		{localexec.Result{Stdout: "2026-10-09T10:33:28.366482000Z " + strings.Repeat("API_KEY=x ", 11000)}, result.LogsLimitExceeded},
	} {
		lines, err := parseContainer(tt.out, 2)
		if lines != nil || err == nil || result.Classify(err).Code() != tt.code {
			t.Fatalf("lines=%v error=%v want code=%s", lines, err, tt.code)
		}
	}
	lines, err := parseContainer(localexec.Result{}, 1)
	if err != nil || lines == nil || len(lines) != 0 {
		t.Fatalf("empty logs=%v error=%v", lines, err)
	}
	lines, err = parseContainer(localexec.Result{Stdout: "2026-10-09T10:33:28.366482000Z partial", Stderr: "2026-10-09T10:33:28.366482000Z \n"}, 2)
	if err != nil || len(lines) != 2 || lines[0].Message != "partial" || lines[1].Message != "" {
		t.Fatalf("partial or empty message lost: %+v error=%v", lines, err)
	}
}

func TestContainerCaptureTruncation(t *testing.T) {
	for _, step := range []string{"inspect", "logs"} {
		e := &scriptedContainer{inspect: validContainer(), logs: localexec.Result{Stdout: containerStdout}}
		if step == "inspect" {
			e.inspect.Truncated = true
		} else {
			e.logs.Truncated = true
		}
		lines, err := (Reader{Inventory: owned("api.container"), Executor: e}).Read(context.Background(), Request{App: "api", Tail: 5})
		if lines != nil || err == nil || result.Classify(err).Code() != result.LogsTruncated {
			t.Fatalf("step=%s lines=%v error=%v", step, lines, err)
		}
	}
}
