package dispatch

import (
	"bytes"
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
	return jobs.Status{Operation: ops.Operation{Kind: ops.Deploy, ID: "op1", PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Queued}, Events: []ops.Event{}, NextCursor: 0}, nil
}

func TestJobClassesAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		op    string
		class Class
		args  string
	}{
		{"apply", Mutating, `{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"key1"}`},
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
	input := `{"schema_version":1,"op":"apply","request_id":"req1","args":{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"key1"}}`
	response, err := NewServer("test", nil).WithJobs(stub, nil).Handle(context.Background(), strings.NewReader(input))
	if err == nil || response.OK || stub.applies != 0 {
		t.Fatal("apply open without operator policy")
	}
}

func TestJobArgumentsRejectInjection(t *testing.T) {
	for _, args := range []string{
		`{"plan_id":"../escape","idempotency_key":"key1"}`,
		`{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"key1","argv":["/bin/sh"]}`,
		`{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"key1","requester":"operator"}`,
		`{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","plan_id":"plan2","idempotency_key":"key1"}`,
		`{"Plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"key1"}`,
		`{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":null}`,
	} {
		raw := `{"schema_version":1,"op":"apply","request_id":"req1","args":` + args + `}`
		if _, err := DecodeRequest([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

func TestAcceptedResponseNeverMeansDeployed(t *testing.T) {
	for _, data := range []string{`{"status":"deployed","operation_id":"op1"}`, `{"status":"accepted","operation_id":"../x"}`, `{"status":"accepted","operation_id":"op1","argv":["sh"]}`} {
		raw := `{"schema_version":1,"command":"brine host apply","ok":true,"data":` + data + `,"error":null}`
		if _, err := DecodeResponse([]byte(raw), "apply"); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestOperationResponseRejectsUnsafeJournal(t *testing.T) {
	status := jobs.Status{Operation: ops.Operation{Kind: ops.Deploy, ID: "op1", PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Queued}, Events: []ops.Event{{Sequence: 1, Kind: "failure", Payload: json.RawMessage(`{"code":"launch_failed"}`)}}, NextCursor: 1}
	valid, _ := json.Marshal(result.Success("brine host operation", status))
	if _, err := DecodeResponse(valid, "operation"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(string(valid), `"code":"launch_failed"`, `"code":"launch_failed","message":"secret"`, 1),
		strings.Replace(string(valid), `"sequence":1`, `"sequence":0`, 1),
		strings.Replace(string(valid), `"next_cursor":1`, `"next_cursor":2`, 1),
		strings.Replace(string(valid), `"state":"queued"`, `"state":"invented"`, 1),
		strings.Replace(string(valid), `"id":"op1"`, `"id":"op1","requester":"spoofed"`, 1),
	} {
		if _, err := DecodeResponse([]byte(raw), "operation"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestAppOperationClassesAndRollbackAuthorization(t *testing.T) {
	for op, want := range map[string]Class{"status": ReadOnly, "rollback": Mutating} {
		if got, ok := ClassOf(op); !ok || got != want {
			t.Fatal(op, got, ok)
		}
	}
	server := NewServer("fixture", nil)
	for _, tc := range []struct {
		op, args string
		code     result.Code
	}{{"status", `{"app":""}`, result.DependencyMissing}, {"rollback", `{"app":"hello","release_id":""}`, result.DispatchOperationRefused}} {
		raw, e := EncodeRequest(Request{SchemaVersion: SchemaVersion, Op: tc.op, RequestID: "fixture", Args: json.RawMessage(tc.args)})
		if e != nil {
			t.Fatal(e)
		}
		response, e := server.Handle(context.Background(), bytes.NewReader(raw))
		if e == nil || response.Error.Code != tc.code {
			t.Fatal(response, e)
		}
	}
	for _, raw := range []string{`{"schema_version":1,"op":"status","request_id":"fixture","args":{"app":"../escape"}}`, `{"schema_version":1,"op":"rollback","request_id":"fixture","args":{"app":"hello","release_id":"../escape"}}`, `{"schema_version":1,"op":"rollback","request_id":"fixture","args":{"app":"hello","release_id":"previous","apply":true}}`} {
		if _, e := DecodeRequest([]byte(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
}
