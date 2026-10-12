//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNativePreparedStopReadRetainsOriginalWithoutReopeningMutation(t *testing.T) {
	state, preparation, lifetime, point := nativeCompletedPointLocksFixture(t)
	ctx := context.Background()
	original, err := state.ReadNativePreparedStop(ctx, point.PointID)
	if err != nil || !validNativeStop(original, point.SourceKind) {
		t.Fatal("original stop not retained", err)
	}
	raw, _ := json.Marshal(original)
	if nativePointHash(raw) != point.StopProofSHA256 {
		t.Fatal("original stop changed")
	}
	if err = lifetime.Release(); err != nil {
		t.Fatal(err)
	}
	retained, err := state.ReadNativePreparedStop(ctx, point.PointID)
	if err != nil || retained != original {
		t.Fatal("expectation read requires mutation permission", err)
	}
	if _, err = state.ReadNativeStoppedDescriptorPreparation(ctx, &NativeStoppedPreparation{preparation: preparation}); err == nil {
		t.Fatal("stop expectation became descriptor authority")
	}
}

func TestNativeRestoreResolutionRefusesMissingOriginalOrGuards(t *testing.T) {
	f, p := storedNativeLiveRestoreFixture(t)
	ctx := context.Background()
	if err := f.store.SaveNativeLiveRestorePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReadNativeRestoreResolutionTarget(ctx, nil, nil, "missing-original"); err == nil {
		t.Fatal("resolution target invented from plan")
	}
	if _, err := f.store.ReadNativeRestoreResolution(ctx, p.ID()); err == nil {
		t.Fatal("apply review used as resolution review")
	}
}
