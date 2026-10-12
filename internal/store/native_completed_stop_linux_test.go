//go:build linux

package store

import (
	"context"
	"testing"
	"time"
)

func TestCompletedNativeStopResumesWithoutReopeningAppendAuthority(t *testing.T) {
	state, preparation, lifetime, point := nativeCompletedPointLocksFixture(t)
	ctx := context.Background()
	resumed, err := state.ResumeNativePreparedPoint(ctx, preparation.preparation, lifetime, point.PointID, nativeAbsentStopFixture())
	if err != nil {
		t.Fatal("completed point could not resume under actual retained locks", err)
	}
	if _, err := state.ReadNativeStoppedPermit(ctx, resumed); err != nil {
		t.Fatal("completed point lost stopped read authority", err)
	}
	if _, err := state.ReadNativeStoppedDescriptorPreparation(ctx, resumed); err != nil {
		t.Fatal("completed point lost actual descriptor guard", err)
	}
	if err := state.RecordNativeStopIntent(ctx, resumed.preparation, NativeStopIntent{Mode: "unpublished_absent", ObservedAt: time.Now().UTC()}); err == nil {
		t.Fatal("completed source reopened append authority")
	}
	if err := lifetime.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.ReadNativeStoppedPermit(ctx, resumed); err == nil {
		t.Fatal("completed source retained authority after lifetime release")
	}
}
