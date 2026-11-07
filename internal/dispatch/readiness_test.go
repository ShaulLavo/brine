package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/result"
)

type readinessFixture struct{}

func (readinessFixture) Read(ctx context.Context) (readiness.Report, error) {
	return (readiness.Reader{}).Read(ctx)
}
func TestReadinessDispatcherClassificationAndExactEmptyPayload(t *testing.T) {
	class, ok := ClassOf("readiness")
	if !ok || class != ReadOnly {
		t.Fatal(class, ok)
	}
	for _, raw := range []string{`{}`, `null`, `[]`, `{"host":"unrelated.example.com"}`, `{"path":"/private"}`, `{"argv":[]}`, `{"target":"fixture","target":"fixture"}`} {
		request := Request{SchemaVersion: 1, Op: "readiness", RequestID: "fixture", Args: json.RawMessage(raw)}
		data, _ := json.Marshal(request)
		_, err := DecodeRequest(data)
		if (err == nil) != (raw == `{}`) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	server := NewServer("fixture", nil).WithJobs(nil, func(_ context.Context, class Class) error {
		if class != ReadOnly {
			t.Fatal("readiness requested mutation authorization")
		}
		return nil
	})
	server.Readiness = readinessFixture{}
	request := `{"schema_version":1,"op":"readiness","request_id":"fixture","args":{}}`
	response, err := server.Handle(t.Context(), strings.NewReader(request))
	if err != nil || !response.OK {
		t.Fatal(response, err)
	}
	raw, _ := json.Marshal(response)
	decoded, err := DecodeResponse(raw, "readiness")
	if err != nil || !decoded.OK {
		t.Fatal(decoded, err)
	}
	report := decoded.Data.(readiness.Report)
	if report.Ready != readiness.UnknownReady || len(report.Findings) != 0 {
		t.Fatal(report)
	}
	response.Data = map[string]any{"ready": true}
	forged, _ := json.Marshal(response)
	if _, err := DecodeResponse(forged, "readiness"); result.ExitCode(err) == 0 {
		t.Fatal("forged report accepted")
	}
}
