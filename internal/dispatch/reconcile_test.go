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
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
)

type reconcileFake struct{ mutate, inspect int }

func (f *reconcileFake) Reconcile(context.Context) (reconcile.Report, error) {
	f.mutate++
	return reconcile.Report{Outcomes: []reconcile.Outcome{}}, nil
}
func (f *reconcileFake) DryRun(context.Context) (reconcile.Report, error) {
	f.inspect++
	return reconcile.Report{DryRun: true, Outcomes: []reconcile.Outcome{}}, nil
}
func TestReconcileDispatcherAuthorizationAndRoundTrip(t *testing.T) {
	f := &reconcileFake{}
	server := NewServer("test", nil)
	server.Reconciler = f
	request := func(dry bool) []byte {
		args, _ := json.Marshal(ReconcileArgs{DryRun: dry})
		raw, err := EncodeRequest(Request{SchemaVersion: 1, Op: "reconcile", RequestID: "request", Args: args})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	envelope, err := server.Handle(context.Background(), bytes.NewReader(request(true)))
	if err != nil || !envelope.OK || f.inspect != 1 || f.mutate != 0 {
		t.Fatalf("envelope %+v err %v fake %+v", envelope, err, f)
	}
	encoded, _ := json.Marshal(envelope)
	if _, err := DecodeResponse(encoded, "reconcile"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Handle(context.Background(), bytes.NewReader(request(false))); err == nil || f.mutate != 0 {
		t.Fatal("unauthorized reconcile ran")
	}
	job := &reconcileJobFake{}
	authorized := server.WithJobs(job, func(_ context.Context, class Class) error {
		if class != Mutating {
			t.Fatal(class)
		}
		return nil
	})
	envelope, err = authorized.Handle(context.Background(), bytes.NewReader(request(false)))
	if err != nil || f.mutate != 0 || job.calls != 1 {
		t.Fatalf("error %v fake %+v", err, f)
	}
	encoded, _ = json.Marshal(envelope)
	decoded, err := DecodeResponse(encoded, "reconcile")
	if err != nil || decoded.Data.(jobs.Accepted).OperationID != "recovery-job" {
		t.Fatalf("accepted round trip: %#v %v", decoded, err)
	}
}
func TestReconcileRejectsUntrustedControls(t *testing.T) {
	for _, args := range []string{`{}`, `{"dry_run":null}`, `{"dry_run":"true"}`, `{"dry_run":true,"operation_id":"other"}`, `{"dry_run":false,"command":"start"}`, `{"dry_run":true,"dry_run":false}`} {
		raw, _ := json.Marshal(Request{SchemaVersion: 1, Op: "reconcile", RequestID: "request", Args: json.RawMessage(args)})
		if _, err := DecodeRequest(raw); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
}

type reconcileJobFake struct {
	jobStub
	calls int
}

func (f *reconcileJobFake) Reconcile(context.Context) (jobs.Accepted, error) {
	f.calls++
	return jobs.Accepted{Status: "accepted", OperationID: "recovery-job"}, nil
}

func TestReconcileReceiptHasClosedStatusShape(t *testing.T) {
	now := time.Now().UTC()
	status := jobs.Status{Operation: ops.Operation{ID: "recovery-job", Kind: ops.Reconcile, State: ops.Preflight, CreatedAt: now, UpdatedAt: now}, Events: []ops.Event{}}
	raw, _ := json.Marshal(result.Success("brine host operation", status))
	if _, err := DecodeResponse(raw, "operation"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"kind":"reconcile"`, `"kind":"unknown"`, 1),
		strings.Replace(string(raw), `"plan_id":""`, `"plan_id":null`, 1),
		strings.Replace(string(raw), `"plan_id":""`, `"plan_id":"sha256:`+strings.Repeat("a", 64)+`"`, 1),
	} {
		if _, err := DecodeResponse([]byte(invalid), "operation"); err == nil {
			t.Fatalf("accepted invalid receipt %s", invalid)
		}
	}
}
