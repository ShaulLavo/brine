package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/reconcile"
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
	authorized := server.WithJobs(nil, func(_ context.Context, class Class) error {
		if class != Mutating {
			t.Fatal(class)
		}
		return nil
	})
	if _, err := authorized.Handle(context.Background(), bytes.NewReader(request(false))); err != nil || f.mutate != 1 {
		t.Fatalf("error %v fake %+v", err, f)
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
