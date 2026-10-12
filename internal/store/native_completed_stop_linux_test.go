//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCompletedNativeStopRejectsRehashedUnsafeOriginal(t *testing.T) {
	for _, field := range []string{"forced", "oom_killed", "exit_code", "source_absent", "restart_jobs_absent", "ingress_closed"} {
		t.Run(field, func(t *testing.T) { checkUnsafeOriginalCompletedStop(t, field) })
	}
}

func checkUnsafeOriginalCompletedStop(t *testing.T, field string) {
	t.Helper()
	state, preparation, lifetime, point := nativeCompletedPointLocksFixture(t)
	ctx := context.Background()
	events, err := readNativePreparationEvents(ctx, state.db, preparation.intent, preparation.raw)
	if err != nil {
		t.Fatal(err)
	}
	var original NativeStopObservation
	if err := json.Unmarshal(events[1].Proof, &original); err != nil {
		t.Fatal(err)
	}
	switch field {
	case "forced":
		original.Forced = true
	case "oom_killed":
		original.OOMKilled = true
	case "exit_code":
		original.ExitCode = 137
	case "source_absent":
		original.SourceAbsent = false
	case "restart_jobs_absent":
		original.RestartJobsAbsent = false
	case "ingress_closed":
		original.IngressClosed = false
	}
	events[1].Proof, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	point.StopProofSHA256 = nativePointHash(events[1].Proof)
	pointRaw, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	events[5].Proof = pointRaw
	if _, err := state.db.ExecContext(ctx, "DROP TRIGGER data_native_prepared_points_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE data_native_prepared_points SET canonical=? WHERE id=?", pointRaw, point.PointID); err != nil {
		t.Fatal(err)
	}
	rewriteNativePreparationChainFixture(t, state, preparation, events)
	if _, err := state.ReadNativePreparedPoint(ctx, point.PointID); err == nil {
		t.Fatal("unsafe original stop survived a coherent point and event chain")
	}
	if _, err := state.ResumeNativePreparedPoint(ctx, preparation.preparation, lifetime, point.PointID, nativeAbsentStopFixture()); err == nil {
		t.Fatal("unsafe original stop reopened despite current valid absence")
	}
}

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
