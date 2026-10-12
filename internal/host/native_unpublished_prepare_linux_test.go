//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestNativePrepareAdoptsOriginalUnpublishedAllocationFence(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	ctx := context.Background()
	state := f.service.Store
	binding := f.plan.Managed.Database.BindingID
	before, err := state.ReadEnginePermit(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := state.ReadManagedPreparation(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	original, err := state.GetOperation(ctx, prepared.OperationID)
	if err != nil || original.State != ops.Succeeded {
		t.Fatal("allocation not genuinely succeeded", err)
	}
	sourceBefore, err := os.ReadFile(f.plan.Managed.Database.SourcePath() + "/config.toml")
	if err != nil {
		t.Fatal(err)
	}
	requester, err := datainit.ParseRequester(f.service.Requester)
	if err != nil {
		t.Fatal(err)
	}
	input := store.NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: binding}
	ref, err := state.SaveNativeTaskInput(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: f.plan.App, SecretRef: ref}, requester.String(), "known-unissued-prepare")
	if err != nil {
		t.Fatal(err)
	}
	for _, stateValue := range []ops.State{ops.Preflight, ops.RecoveryRequired} {
		if err := state.SetOperationState(ctx, failed.ID, stateValue); err != nil {
			t.Fatal(err)
		}
	}
	failedBefore, err := state.GetOperation(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedEvents, err := state.EventsAfter(ctx, failed.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: f.plan.App, SecretRef: ref}, requester.String(), "fresh-unpublished-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	work := nativePrepareWork{runtime: nativeBackupRuntime{state: state, stateRoot: f.stateRoot, home: f.home}, input: input, operation: op}
	defer func() {
		if err := work.close(); err != nil {
			t.Error(err)
		}
	}()
	if err := work.acquire(ctx); err != nil {
		t.Fatal("fresh unpublished preparation rejected its original allocation fence", err)
	}
	if work.fence.OperationID != prepared.OperationID || work.fence.OperationID == op.ID {
		t.Fatal("original fence owner changed")
	}
	if err := state.RecordNativeStopIntent(ctx, work.native, store.NativeStopIntent{Mode: "unpublished_absent", ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, f.stateRoot, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lifetime.Release() }()
	stopped, err := state.RecordNativeStoppedPreparation(ctx, work.native, lifetime, store.NativeStopObservation{Mode: "unpublished_absent", FinishedAt: time.Now().UTC(), SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true})
	if err != nil || stopped.OperationID() != op.ID || stopped.FenceOwnerOperationID() != original.ID {
		t.Fatal("stopped preparation confused effect owner and fence owner", err)
	}
	if _, err := state.ReadNativeStoppedPermit(ctx, stopped); err != nil {
		t.Fatal("derived owner failed canonical stopped readback", err)
	}
	other, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: f.plan.App, SecretRef: ref}, requester.String(), "other-unpublished-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, other.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if _, err := state.HoldNativePreparationFence(ctx, binding, other.ID); err == nil {
		t.Fatal("second prepare bypassed an existing unknown claim")
	}
	after, err := state.ReadEnginePermit(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		t.Fatal("preparation changed original allocation, material, runtime or fences")
	}
	originalAfter, err := state.GetOperation(ctx, original.ID)
	if err != nil || originalAfter != original {
		t.Fatal("original allocation outcome changed", err)
	}
	failedAfter, err := state.GetOperation(ctx, failed.ID)
	if err != nil || failedAfter != failedBefore {
		t.Fatal("known unissued failure changed", err)
	}
	afterEvents, err := state.EventsAfter(ctx, failed.ID, 0, 100)
	a, _ = json.Marshal(failedEvents)
	b, _ = json.Marshal(afterEvents)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("known unissued failure events changed", err)
	}
	sourceAfter, err := os.ReadFile(f.plan.Managed.Database.SourcePath() + "/config.toml")
	if err != nil || !bytes.Equal(sourceBefore, sourceAfter) {
		t.Fatal("preparation changed source", err)
	}
}

func TestNativePrepareRefusesUnprovenUnpublishedFence(t *testing.T) {
	for _, mode := range []string{"missing_material", "foreign_owner", "replacement_fence"} {
		t.Run(mode, func(t *testing.T) { checkNativePrepareRefusal(t, mode) })
	}
}

func checkNativePrepareRefusal(t *testing.T, mode string) {
	t.Helper()

	f := freshNativeMaterialFixture(t)
	ctx := context.Background()
	state, binding := f.service.Store, f.plan.Managed.Database.BindingID
	prepared, err := state.ReadManagedPreparation(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "missing_material" {
		packet := freshNativeMaterialPacket(t)
		defer clear(packet)
		deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
		fence, err := state.ReadManagedPreparationFence(ctx, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if err := state.ReleaseDataFence(ctx, fence, prepared.OperationID); err != nil {
			t.Fatal(err)
		}
		owner := prepared.OperationID
		if mode == "foreign_owner" {
			owner = "foreign-operation"
		}
		if _, err := state.HoldDataFence(ctx, f.plan.Managed.Database.DatabaseID, owner); err != nil {
			t.Fatal(err)
		}
	}
	requester, err := datainit.ParseRequester(f.service.Requester)
	if err != nil {
		t.Fatal(err)
	}
	input := store.NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: binding}
	ref, err := state.SaveNativeTaskInput(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: f.plan.App, SecretRef: ref}, requester.String(), "unproven-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fencesBefore, err := state.ReadManagedApp(ctx, f.plan.App)
	if err != nil {
		t.Fatal(err)
	}
	work := nativePrepareWork{runtime: nativeBackupRuntime{state: state, stateRoot: f.stateRoot, home: f.home}, input: input, operation: op}
	defer func() { _ = work.close() }()
	if err := work.acquire(ctx); err == nil {
		t.Fatal("unproven fence admitted")
	}
	fencesAfter, err := state.ReadManagedApp(ctx, f.plan.App)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fencesBefore)
	b, _ := json.Marshal(fencesAfter)
	if !bytes.Equal(a, b) {
		t.Fatal("refusal changed fences")
	}
}
