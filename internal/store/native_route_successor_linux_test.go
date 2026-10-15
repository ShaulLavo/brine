//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeWithdrawalSuccessorRequiresCompletedTransitionFromPoint(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	common := f.input.Common()
	point := NativePreparedPointView{PointID: common.PreparedPoint.ID, BindingID: common.Scope.BindingID, EpochID: common.PreparedPoint.EpochID, FenceID: common.FenceID}
	successor := func(p NativePreparedPointView, epoch data.ReplicaEpochID, runtime string) error {
		tx, err := f.store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		return nativeWithdrawalSuccessor(ctx, tx, p, epoch, runtime)
	}
	if successor(point, "", "") == nil {
		t.Fatal("prepared transition counted as a successor")
	}
	activated := activationCursorFixture(t, f)
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	activated, err = f.store.activateEngineTransitionTx(ctx, tx, f.preparation, activated, f.runtime, f.allocation)
	if err != nil || tx.Commit() != nil {
		t.Fatal("activation failed", err)
	}
	if successor(point, "", "") == nil {
		t.Fatal("unfinished transition counted as a successor")
	}
	finish := advanceStoreTransition(t, f, activated, data.EngineTransitionFinishIntent, data.EngineLayoutPromoted)
	tx, cancel2, err := f.store.beginWrite(ctx)
	defer cancel2()
	if err != nil {
		t.Fatal(err)
	}
	proof := storeTransitionEvidence(common.Scope, f.runtime.EpochID, data.EngineCompletionEvidence, 9)
	if _, err = f.store.finishEngineTransitionTx(ctx, tx, f.preparation, finish, proof, false); err != nil || tx.Commit() != nil {
		t.Fatal("completion failed", err)
	}
	raw, err := json.Marshal(f.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := successor(point, f.runtime.EpochID, nativePointHash(raw)); err != nil {
		t.Fatal("completed successor refused", err)
	}
	if successor(point, "", "") != nil {
		t.Fatal("completed successor refused its own target")
	}
	for name, p := range map[string]NativePreparedPointView{
		"other-point": {PointID: "ffffffffffffffffffffffffffffffff", BindingID: point.BindingID, EpochID: point.EpochID, FenceID: point.FenceID},
		"other-epoch": {PointID: point.PointID, BindingID: point.BindingID, EpochID: f.runtime.EpochID, FenceID: point.FenceID},
		"other-fence": {PointID: point.PointID, BindingID: point.BindingID, EpochID: point.EpochID, FenceID: "ffffffffffffffffffffffffffffffff"},
	} {
		if successor(p, "", "") == nil {
			t.Fatal("foreign point scope accepted", name)
		}
	}
	if successor(point, point.EpochID, "") == nil || successor(point, f.runtime.EpochID, "sha256:other") == nil {
		t.Fatal("successor accepted a different current runtime")
	}
}
