//go:build linux

package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeWithdrawalAfterCompletedRestoreAllowsPolicyRestoration(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := t.Context()
	c := f.input.Common()
	source, _ := c.Base.Committed()
	var app string
	if err := f.store.db.QueryRowContext(ctx, "SELECT app FROM data_incarnations WHERE id=?", c.Scope.IncarnationID).Scan(&app); err != nil {
		t.Fatal(err)
	}
	point := NativePreparedPointView{PointID: c.PreparedPoint.ID, FenceID: c.FenceID, BindingID: c.Scope.BindingID, EpochID: source.EpochID, DatabaseID: c.Scope.DatabaseID, IncarnationID: c.Scope.IncarnationID, RuntimeSHA256: source.RuntimeSHA256, PolicySHA256: strings.TrimPrefix(c.Scope.PolicyHash, "sha256:")}
	head := ManagedRouteState{App: app, BindingID: c.Scope.BindingID, EpochID: source.EpochID, RuntimeSHA256: source.RuntimeSHA256}
	restoredPolicy := "sha256:" + strings.Repeat("9", 64)
	old := activationCursorFixture(t, f)
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	activated, err := f.store.activateEngineTransitionTx(ctx, tx, f.preparation, old, f.runtime, f.allocation)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, cancel2, err := f.store.beginWrite(ctx)
	defer cancel2()
	if err != nil {
		t.Fatal(err)
	}
	if err := nativeRouteWithdrawalCurrent(ctx, tx, head, restoredPolicy, point); err == nil {
		t.Fatal("unfinished transition accepted after policy change")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	finish := advanceStoreTransition(t, f, activated, data.EngineTransitionFinishIntent, data.EngineLayoutPromoted)
	tx, cancel3, err := f.store.beginWrite(ctx)
	defer cancel3()
	if err != nil {
		t.Fatal(err)
	}
	proof := storeTransitionEvidence(c.Scope, f.runtime.EpochID, data.EngineCompletionEvidence, 9)
	if _, err := f.store.finishEngineTransitionTx(ctx, tx, f.preparation, finish, proof, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := nativeRouteWithdrawalCurrent(ctx, tx, head, restoredPolicy, point); err != nil {
		t.Fatal("completed transition withdrawal rejected restored operator policy", err)
	}
}
