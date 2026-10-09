package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

type jobStub struct{ applies, reads int }

func (s *jobStub) Apply(context.Context, string, string) (jobs.Accepted, error) {
	s.applies++
	return jobs.Accepted{Status: "accepted", OperationID: "op1"}, nil
}
func (s *jobStub) Operation(context.Context, string, uint64) (jobs.Status, error) {
	s.reads++
	return jobs.Status{Operation: ops.Operation{ID: "op1", PlanID: "plan1", State: ops.Queued}, Events: []ops.Event{}, NextCursor: 0}, nil
}

func TestJobClassesAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		op    string
		class Class
		args  string
	}{
		{"apply", Mutating, `{"plan_id":"plan1","idempotency_key":"key1"}`},
		{"operation", ReadOnly, `{"operation_id":"op1","after_cursor":0}`},
	} {
		t.Run(tc.op, func(t *testing.T) {
			if class, ok := ClassOf(tc.op); !ok || class != tc.class {
				t.Fatalf("class=%s known=%v", class, ok)
			}
			stub := &jobStub{}
			input, _ := EncodeRequest(Request{SchemaVersion: 1, Op: tc.op, RequestID: "req1", Args: json.RawMessage(tc.args)})
			server := NewServer("test", nil).WithJobs(stub, func(_ context.Context, class Class) error {
				if class != ReadOnly {
					return result.New(result.PolicyRefused, nil)
				}
				return nil
			})
			response, err := server.Handle(context.Background(), strings.NewReader(string(input)))
			if tc.class == Mutating {
				if err == nil || response.OK || stub.applies != 0 {
					t.Fatal("mutation bypassed policy")
				}
			} else if err != nil || !response.OK || stub.reads != 1 {
				t.Fatal("read refused", err)
			}
			server = NewServer("test", nil).WithJobs(stub, func(context.Context, Class) error { return nil })
			response, err = server.Handle(context.Background(), strings.NewReader(string(input)))
			if err != nil || !response.OK {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(response)
			decoded, err := DecodeResponse(raw, tc.op)
			if err != nil || !decoded.OK {
				t.Fatalf("cannot decode response: %s %v", raw, err)
			}
		})
	}
}

func TestApplyClosedWithoutPolicy(t *testing.T) {
	stub := &jobStub{}
	input := `{"schema_version":1,"op":"apply","request_id":"req1","args":{"plan_id":"plan1","idempotency_key":"key1"}}`
	response, err := NewServer("test", nil).WithJobs(stub, nil).Handle(context.Background(), strings.NewReader(input))
	if err == nil || response.OK || stub.applies != 0 {
		t.Fatal("apply open without operator policy")
	}
}

func TestJobArgumentsRejectInjection(t *testing.T) {
	for _, args := range []string{
		`{"plan_id":"../escape","idempotency_key":"key1"}`,
		`{"plan_id":"plan1","idempotency_key":"key1","argv":["/bin/sh"]}`,
		`{"plan_id":"plan1","idempotency_key":"key1","requester":"operator"}`,
		`{"plan_id":"plan1","plan_id":"plan2","idempotency_key":"key1"}`,
		`{"Plan_id":"plan1","idempotency_key":"key1"}`,
		`{"plan_id":"plan1","idempotency_key":null}`,
	} {
		raw := `{"schema_version":1,"op":"apply","request_id":"req1","args":` + args + `}`
		if _, err := DecodeRequest([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}
