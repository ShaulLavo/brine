//go:build linux

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativePreparedFrontendStopRequiresExactOriginalTaskAndGuard(t *testing.T) {
	ctx := context.Background()
	state, plan, _, resolver := managedResolutionFixture(t)
	finishManagedResolution(t, state, resolver.ID)
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := state.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: plan.Managed.Database.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: plan.App, SecretRef: reference}, requester.String(), "prepared-frontend-stop")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, operation.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fence, err := state.HoldDataFence(ctx, plan.Managed.Database.DatabaseID, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := state.AcquireEnginePreparation(ctx, plan.Managed.Database.BindingID, fence.ID, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Close() }()
	preparation, err := state.BeginNativePreparation(ctx, guard, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.RecordNativePreparedFrontendStop(ctx, preparation, true); err == nil {
		t.Fatal("absence without the original stop intent admitted")
	}
	if err := state.RecordNativePreparedFrontendStop(ctx, preparation, false); err != nil {
		t.Fatal("actual protected frontend intent", err)
	}
	if err := state.RecordNativePreparedFrontendStop(ctx, preparation, false); err == nil {
		t.Fatal("frontend stop intent repeated")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := state.RecordNativePreparedFrontendStop(ctx, preparation, true); err == nil {
		t.Fatal("released guard recorded frontend absence")
	}
}
