package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
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
