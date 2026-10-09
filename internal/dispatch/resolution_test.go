package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

type resolutionFake struct {
	jobStub
	calls int
}

func (f *resolutionFake) Resolve(context.Context, string, string) (jobs.Accepted, error) {
	f.calls++
	return jobs.Accepted{Status: "accepted", OperationID: "resolution-job"}, nil
}

func TestResolutionD8AuthorizationAndClosedRequest(t *testing.T) {
	fake := &resolutionFake{}
	raw, _ := json.Marshal(Request{SchemaVersion: 1, Op: "resolve", RequestID: "request", Args: json.RawMessage(`{"operation_id":"source","idempotency_key":"key"}`)})
	for _, authorize := range []Authorization{nil, func(context.Context, Class) error { return result.New(result.DispatchOperationRefused, nil) }} {
		server := NewServer("fixture", nil).WithJobs(fake, authorize)
		if _, err := server.Handle(context.Background(), bytes.NewReader(raw)); err == nil || fake.calls != 0 {
			t.Fatal("unauthorized resolution executed")
		}
	}
	server := NewServer("fixture", nil).WithJobs(fake, func(_ context.Context, class Class) error {
		if class != Mutating {
			t.Fatal(class)
		}
		return nil
	})
	response, err := server.Handle(context.Background(), bytes.NewReader(raw))
	if err != nil || fake.calls != 1 {
		t.Fatal(response, err)
	}
	encoded, _ := json.Marshal(response)
	if _, err = DecodeResponse(encoded, "resolve"); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"operation_id":"source","idempotency_key":"key","force":true}`, `{"operation_id":"source","idempotency_key":"key","shell":"rm"}`, `{"operation_id":"source","idempotency_key":"key","purge":true}`, `{"operation_id":null,"idempotency_key":"key"}`} {
		request, _ := json.Marshal(Request{SchemaVersion: 1, Op: "resolve", RequestID: "request", Args: json.RawMessage(args)})
		if _, err := DecodeRequest(request); err == nil {
			t.Fatal("accepted untrusted resolution controls", args)
		}
	}
}

func TestResolutionOperationStatusClosedLink(t *testing.T) {
	now := time.Now().UTC()
	status := jobs.Status{Operation: ops.Operation{ID: "successor", PlanID: "sha256:" + strings.Repeat("a", 64), Kind: ops.Resolve, App: "hello", RecoveryOf: "source", State: ops.Queued, CreatedAt: now, UpdatedAt: now}, Events: []ops.Event{}, NextCursor: 0}
	raw, err := json.Marshal(result.Success("brine host operation", status))
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(raw, "operation")
	if err != nil || response.Data.(jobs.Status).Operation.RecoveryOf != "source" {
		t.Fatal(response, err)
	}
	for _, link := range []string{"", "../foreign"} {
		status.Operation.RecoveryOf = link
		raw, _ = json.Marshal(result.Success("brine host operation", status))
		if _, err := DecodeResponse(raw, "operation"); err == nil {
			t.Fatal("accepted invalid resolution link", link)
		}
	}
}
