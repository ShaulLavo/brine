//go:build linux

package store

import (
	"strings"
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
	point := nativeVerifiedPointFixture(&NativePreparation{intent: nativePreparationIntent{BindingID: f.runtime.BindingID, EpochID: f.input.Common().Base.Head().EpochID}})
	point.PointID, point.OperationID = strings.Repeat("d", 32), "new-capture"
	abandonExec(t, s.db, "INSERT INTO data_native_verified_points VALUES(?,?,?,?,?)", point.PointID, point.OperationID, point.BindingID, point.EpochID, abandonJSON(t, point))
	pending, err := countUnresolvedNativeCaptures(ctx, s.db, f.runtime.BindingID)
	if err != nil || pending != 0 {
		t.Fatal("old capture failure still dominates a fresh verified point", pending, err)
	}
}
