package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/target"
)

type logFake struct{ requests []logs.Request }

func (f *logFake) Read(_ context.Context, r logs.Request) ([]logs.Line, error) {
	f.requests = append(f.requests, r)
	return []logs.Line{{Timestamp: "2026-10-09T00:00:00Z", Priority: 6, Message: "ready"}}, nil
}
func TestLogsDispatch(t *testing.T) {
	server := NewServer("test", nil)
	fake := &logFake{}
	server.Logs = fake
	request := `{"schema_version":1,"op":"logs","request_id":"logs-test","args":{"app":"api","tail":5}}`
	response, err := server.Handle(context.Background(), strings.NewReader(request))
	if err != nil || !response.OK || len(fake.requests) != 1 {
		t.Fatalf("%+v %v", response, err)
	}
	raw, _ := json.Marshal(response)
	decoded, err := DecodeResponse(raw, "logs")
	if err != nil || len(decoded.Data.([]logs.Line)) != 1 {
		t.Fatalf("%+v %v", decoded, err)
	}
	if class, ok := ClassOf("logs"); !ok || class != ReadOnly {
		t.Fatal("logs is not read-only")
	}
	for _, args := range []string{`{"app":"api;id","tail":5}`, `{"app":"api","tail":1001}`, `{"app":"api","tail":5,"unit":"other.service"}`, `{"app":"api","tail":5,"since":null}`, `{"app":"api","tail":5,"since":"yesterday"}`} {
		bad := strings.Replace(request, `{"app":"api","tail":5}`, args, 1)
		response, err := server.Handle(context.Background(), strings.NewReader(bad))
		if err == nil || response.Error.Code != result.DispatchInvalidRequest || len(fake.requests) != 1 {
			t.Fatalf("accepted %s %+v %v", args, response, err)
		}
	}
}

type logInventory struct{}

func (logInventory) Collect(context.Context) (target.Snapshot, error) {
	return target.Snapshot{Apps: target.Known([]target.App{{Name: "api", QuadletUnits: target.Known([]target.Unit{{Name: "api.container"}})}})}, nil
}

type logExecutionFailure struct{ kind localexec.ErrorKind }

func (f logExecutionFailure) Execute(context.Context, localexec.Command) (localexec.Result, error) {
	return localexec.Result{Stdout: "API_KEY=planted-secret", Stderr: "Bearer planted-secret", ExitCode: 1}, &localexec.Error{Kind: f.kind, ExitCode: 1}
}

func TestLogsCollectionFailureTransport(t *testing.T) {
	for _, tt := range []struct {
		kind localexec.ErrorKind
		code result.Code
	}{
		{localexec.Failed, result.LogsJournalFailed},
		{localexec.Timeout, result.LogsJournalTimeout},
		{localexec.NotFound, result.LogsJournalUnavailable},
	} {
		server := NewServer("test", nil)
		server.Logs = logs.Reader{Inventory: logInventory{}, Executor: logExecutionFailure{tt.kind}}
		response, err := server.Handle(context.Background(), strings.NewReader(`{"schema_version":1,"op":"logs","request_id":"logs-test","args":{"app":"api","tail":5}}`))
		if err == nil || response.OK || response.Error == nil || response.Error.Code != tt.code || response.Data != nil {
			t.Fatalf("response=%+v error=%v", response, err)
		}
		raw, err := json.Marshal(response)
		if err != nil || strings.Contains(string(raw), "planted-secret") {
			t.Fatal("transport leaked failed subprocess output")
		}
		decoded, err := DecodeResponse(raw, "logs")
		if err != nil || decoded.Error == nil || decoded.Error.Code != tt.code || decoded.Data != nil {
			t.Fatalf("decoded=%+v error=%v", decoded, err)
		}
	}
}
