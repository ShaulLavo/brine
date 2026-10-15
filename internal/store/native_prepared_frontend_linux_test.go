//go:build linux

package store

import (
	"context"
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/plan"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativePreparedFrontendStopRequiresExactOriginalTaskAndGuard(t *testing.T) {
	testNativePreparedFrontendStop(t, false)
}

func TestNativePreparedFrontendStopAfterMaterialActivation(t *testing.T) {
	testNativePreparedFrontendStop(t, true)
}

func testNativePreparedFrontendStop(t *testing.T, renewed bool) {
	t.Helper()
	ctx := context.Background()
	state, plan, _, resolver := managedResolutionFixture(t)
	finishManagedResolution(t, state, resolver.ID)
	if renewed {
		recordFrontendFixtureMaterialActivation(t, state, plan)
	}
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

func recordFrontendFixtureMaterialActivation(t *testing.T, state *Store, planned plan.Plan) {
	t.Helper()
	head, runtime, err := state.ReadEngineHead(t.Context(), planned.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Secrets = materialVersions(runtime.Secrets, 2)
	raw, _ := json.Marshal(runtime)
	now := time.Now().UTC()
	p := nativematerial.ActivationPlan{ID: strings.Repeat("a", 32), App: planned.App, Requester: "fixture", DeliveryOperationID: "material-delivery", Head: head, DesiredRevision: 1, PolicyHash: planned.PolicyHash, Secrets: runtime.Secrets, RestartRequired: true, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	receipt := nativematerial.ActivationReceipt{PlanID: p.ID, BindingID: head.BindingID, EpochID: head.EpochID, RuntimeVersion: head.Version + 1, RuntimeSHA256: nativeCanonicalHash(raw)}
	abandonExec(t, state.db, "INSERT INTO data_native_material_activation_plans VALUES(?,?)", p.ID, abandonJSON(t, p))
	abandonExec(t, state.db, "INSERT INTO data_engine_runtime_commits VALUES(?,?,?)", head.BindingID, receipt.RuntimeVersion, raw)
	abandonExec(t, state.db, "INSERT INTO data_native_material_activations VALUES(?,?,?,?)", p.ID, head.BindingID, receipt.RuntimeVersion, abandonJSON(t, receipt))
}
