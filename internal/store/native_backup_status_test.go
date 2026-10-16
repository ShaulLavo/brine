//go:build linux

package store

import (
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeBackupStatusIgnoresHistoricalCaptureFailures(t *testing.T) {
	f := newNativeTransitionFixture(t)
	s, ctx := f.store, t.Context()
	requester, _ := datainit.ParseRequester(f.input.Common().Scope.Requester)
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativeCapture, BindingID: f.runtime.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,'recovery_required',?,?,?,?,?)", "old-capture", requester.String(), "old-capture", created, created, ops.NativeCapture, "managed", ref)
	pending, err := countUnresolvedNativeCaptures(ctx, s.db, f.runtime.BindingID, nil)
	if err != nil || pending != 1 {
		t.Fatal("failure without a verified point hidden", pending, err)
	}
	capturedAt := time.Now().UTC()
	pending, err = countUnresolvedNativeCaptures(ctx, s.db, f.runtime.BindingID, &capturedAt)
	if err != nil || pending != 0 {
		t.Fatal("old capture failure still dominates a fresh verified point", pending, err)
	}
	for _, state := range []ops.State{ops.Failed, ops.RecoveryRequired, ops.Preflight, ops.LaunchUnknown} {
		id := "capture-" + string(state)
		at := timestamp()
		if state == ops.Preflight || state == ops.LaunchUnknown {
			at = created
		}
		abandonExec(t, s.db, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,?,?,?,?,?,?)", id, requester.String(), id, state, at, at, ops.NativeCapture, "managed", ref)
	}
	pending, err = countUnresolvedNativeCaptures(ctx, s.db, f.runtime.BindingID, &capturedAt)
	if err != nil || pending != 4 {
		t.Fatal("recent failures or active captures hidden by historical success", pending, err)
	}
}
