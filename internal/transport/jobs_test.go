package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
)

type operationStub struct{ applied bool }

func (s *operationStub) Apply(_ context.Context, planID, key string) (jobs.Accepted, error) {
	s.applied = planID == "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" && key == "key1"
	return jobs.Accepted{Status: "accepted", OperationID: "op1"}, nil
}
func (s *operationStub) Operation(_ context.Context, id string, cursor uint64) (jobs.Status, error) {
	return jobs.Status{Operation: ops.Operation{ID: id, PlanID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: ops.Succeeded}, Events: []ops.Event{}, NextCursor: cursor}, nil
}

type dispatcherSSH struct {
	server *dispatch.Server
	calls  int
}

func (s *dispatcherSSH) RunInput(ctx context.Context, c localexec.Command) (localexec.Output, error) {
	s.calls++
	response, err := s.server.Handle(ctx, bytes.NewReader(c.Stdin))
	if err != nil {
		return localexec.Output{}, err
	}
	raw, err := json.Marshal(response)
	return localexec.Output{Stdout: raw}, err
}
func TestApplyAndOperationThroughSSHTransport(t *testing.T) {
	stub := &operationStub{}
	runner := &dispatcherSSH{server: dispatch.NewServer("fixture", nil).WithJobs(stub, func(context.Context, dispatch.Class) error { return nil })}
	client := Client{Runner: runner, KnownHostsDir: filepath.Join(t.TempDir(), "pins"), LookPath: func(string) (string, error) { return "/fixture/ssh", nil }}
	response, err := client.Call(context.Background(), validTarget(), dispatch.Request{SchemaVersion: 1, Op: "apply", RequestID: "apply1", Args: json.RawMessage(`{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"key1"}`)})
	if err != nil || !stub.applied {
		t.Fatal("apply not transported", err)
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !ok || accepted.Status != "accepted" || accepted.OperationID != "op1" {
		t.Fatalf("%+v", response)
	}
	response, err = client.Call(context.Background(), validTarget(), dispatch.Request{SchemaVersion: 1, Op: "operation", RequestID: "poll1", Args: json.RawMessage(`{"operation_id":"op1","after_cursor":42}`)})
	if err != nil {
		t.Fatal(err)
	}
	status, ok := response.Data.(jobs.Status)
	if !ok || status.Operation.State != ops.Succeeded || status.NextCursor != 42 || runner.calls != 2 {
		t.Fatalf("%+v calls=%d", response, runner.calls)
	}
}
