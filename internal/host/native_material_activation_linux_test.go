//go:build linux

package host

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func initializedMaterialFixture(t *testing.T) nativeMaterialFixture {
	t.Helper()
	f := freshNativeMaterialFixture(t)
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	delivered := deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	ctx := t.Context()
	_, runtime, err := f.service.Store.ReadEngineHead(ctx, delivered.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.State = data.EngineInitialized
	runtime.Database = data.NativeDatabaseIdentity(strings.Repeat("d", 64))
	runtime.Publisher = data.NativePublisherIdentity(strings.Repeat("a", 64))
	runtime.Names = []string{f.desired.SpacetimeDB.Database}
	runtime.Secrets.Publisher.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	prepared, err := f.service.Store.ReadManagedPreparation(ctx, runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := f.service.Store.ReadManagedPreparationFence(ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.CommitEngineRuntime(ctx, runtime, fence, prepared.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.ReleaseDataFence(ctx, fence, prepared.OperationID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Versions.Caddy = target.Known("2.10.2")
	hash, err := managedRuntimeHash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	managed := *f.plan.Managed
	managed.State, managed.RuntimeSHA256, managed.EngineUnitHash = data.EngineInitialized, hash, runtime.UnitSHA256
	desired, err := f.service.Store.ReadManagedDesired(ctx, runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	in := plan.Input{Desired: f.desired, Snapshot: snapshot, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{{App: f.plan.App, ID: "material-fixture-release", Desired: f.desired, Image: managed.EngineImage, HostPort: managed.EnginePort, Secrets: []plan.SecretBinding{}, Units: []target.Unit{}, CaddyFile: target.CaddyFile{Name: f.plan.App + ".caddy", Hash: "sha256:" + strings.Repeat("b", 64)}, Managed: &managed}}}, Image: managed.EngineImage, Managed: &plan.ManagedFacts{Database: &managed.Database, Archive: &managed.Archive, Runtime: &runtime, EngineImage: managed.EngineImage, Fences: []data.QuiescenceFence{}, NativeState: target.Known("stopped"), OwnedNativeState: target.Known("stopped"), LoggingPrerequisite: target.Known(true), LoggingRootSHA256: strings.Repeat("a", 64), LoggingConfigSHA256: strings.Repeat("b", 64), Intent: &plan.ManagedIntentFacts{Revision: desired.Revision, EpochID: desired.EpochID, RuntimeSHA256: desired.RuntimeSHA256}}}
	stop, err := plan.BuildEngineStop(in)
	if err != nil || len(stop.Conflicts) != 0 {
		t.Fatal("stop plan", err, stop.Conflicts)
	}
	if _, err = f.service.Store.SavePlan(ctx, stop, f.desired); err != nil {
		t.Fatal(err)
	}
	operation, _, err := f.service.Store.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: stop.Hash}, f.service.Requester, "material-fixture-stop")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing} {
		if err = f.service.Store.SetOperationState(ctx, operation.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	wanted := store.ManagedDesiredState{BindingID: runtime.BindingID, EpochID: runtime.EpochID, RuntimeSHA256: hash, Revision: desired.Revision + 1, PlanID: stop.Hash, OperationID: operation.ID}
	if _, err = f.service.Store.CommitManagedDesired(ctx, operation.ID, desired.Revision, wanted); err != nil {
		t.Fatal(err)
	}
	return f
}

func materialActivationCall(t *testing.T, f nativeMaterialFixture, operation string, args any) any {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: operation, RequestID: "material-activation-test", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	server := dispatch.NewServer("fixture", nil).WithJobs(nil, f.service.Authorize)
	server.NativeMaterial = nativeMaterialOperations{service: f.service}
	response, err := server.Handle(t.Context(), bytes.NewReader(request))
	if err != nil || !response.OK {
		t.Fatal(operation, err, response.Error)
	}
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := dispatch.DecodeResponse(wire, operation)
	if err != nil {
		t.Fatal("wire receipt", err)
	}
	return decoded.Data
}

func TestNativeMaterialExpiredPublisherCanStartAndRenewThroughReviewedActivation(t *testing.T) {
	f := initializedMaterialFixture(t)
	ctx := t.Context()
	before, runtime, err := f.service.Store.ReadEngineHead(ctx, f.plan.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := f.service.Store.ReadEnginePermit(ctx, runtime.BindingID)
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("expired publisher blocked start", err)
	}
	signing, err := readEngineMaterial(ctx, f.stateRoot, permit)
	if err != nil || len(signing.publisher) != 0 {
		t.Fatal("startup consumed publisher", err)
	}
	signing.clear()
	if _, err := readPublisherMaterial(ctx, f.stateRoot, permit); err == nil {
		t.Fatal("publication accepted expired lease")
	}
	request, _ := json.Marshal(engineReplayInput{Runtime: runtime})
	if _, err := readEngineReplayInput(bytes.NewReader(request)); err != nil {
		t.Fatal("copy startup refused expired publisher", err)
	}
	materialRoot, err := engineOpenRoot(f.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = materialRoot.Close() }()
	oldKey := filepath.Join("engine-secrets", string(runtime.BindingID), "signing-private", "v1.secret")
	original, err := materialRoot.ReadFile(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(original)
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	delivery := deliverNativeMaterialFixture(t, f, strings.Repeat("2", 32), packet)
	reviewed := materialActivationCall(t, f, "native_material_plan", dispatch.NativeMaterialPlanArgs{App: f.plan.App, DeliveryOperationID: delivery.OperationID}).(nativematerial.ActivationPlan)
	if !reviewed.RestartRequired || reviewed.Head != before {
		t.Fatal("review omitted signing reload or source head")
	}
	afterDelivery, _, err := f.service.Store.ReadEngineHead(ctx, runtime.BindingID)
	if err != nil || afterDelivery != before {
		t.Fatal("delivery activated itself", err)
	}
	stale := materialActivationCall(t, f, "native_material_plan", dispatch.NativeMaterialPlanArgs{App: f.plan.App, DeliveryOperationID: delivery.OperationID}).(nativematerial.ActivationPlan)
	args := dispatch.NativeMaterialActivateArgs{PlanID: reviewed.ID}
	receipt := materialActivationCall(t, f, "native_material_activate", args).(nativematerial.ActivationReceipt)
	after, active, err := f.service.Store.ReadEngineHead(ctx, runtime.BindingID)
	if err != nil || after.Version != before.Version+1 || after.RuntimeSHA256 != receipt.RuntimeSHA256 || active.Secrets != delivery.Secrets || after.EpochID != before.EpochID {
		t.Fatal("activation did not select one new runtime", err)
	}
	permit, err = f.service.Store.ReadEnginePermit(ctx, runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := readPublisherMaterial(ctx, f.stateRoot, permit)
	if err != nil || len(renewed.publisher) == 0 {
		t.Fatal("renewed publication material refused", err)
	}
	stage := moduleStageInput{Action: moduleStagePreflight, Runtime: permit.Runtime, Token: string(renewed.publisher), WasmSHA256: string(permit.Runtime.Module.Wasm), ProgramWireHash: string(permit.Runtime.Module.Program), SchemaSHA256: permit.Runtime.Module.Schema.SHA256}
	stageRaw, _ := json.Marshal(stage)
	if _, err := readModuleStageInput(bytes.NewReader(stageRaw)); err != nil {
		t.Fatal("renewed lease did not admit publication", err)
	}
	clear(stageRaw)
	renewed.clear()
	if _, err := (nativeMaterialOperations{service: f.service}).ActivateMaterial(ctx, dispatch.NativeMaterialActivateArgs{PlanID: stale.ID}); result.Classify(err).Code() != result.Conflict {
		t.Fatal("stale review applied", err)
	}
	repeat := materialActivationCall(t, f, "native_material_activate", args).(nativematerial.ActivationReceipt)
	if repeat != receipt {
		t.Fatal("activation replay changed receipt")
	}
	retained, err := materialRoot.ReadFile(oldKey)
	defer clear(retained)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatal("older signing recovery version changed", err)
	}
	desired, err := f.service.Store.ReadManagedDesired(ctx, runtime.BindingID)
	if err != nil || !desired.Current(after.BindingID, after.EpochID, after.RuntimeSHA256) || desired.EngineRunning || desired.FrontendRunning || desired.IngressServing {
		t.Fatal("activation granted unreviewed start", err)
	}
}

func TestPublicationStageRequiresLivePublisherLease(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	request := moduleStageInput{Action: moduleStagePreflight, Runtime: permit.Runtime, Token: "synthetic.header.signature", WasmSHA256: string(permit.Runtime.Module.Wasm), ProgramWireHash: string(permit.Runtime.Module.Program), SchemaSHA256: permit.Runtime.Module.Schema.SHA256}
	raw, _ := json.Marshal(request)
	if _, err := readModuleStageInput(bytes.NewReader(raw)); err != nil {
		t.Fatal("live lease rejected", err)
	}
	request.Runtime.Secrets.Publisher.ExpiresAt = time.Now().UTC().Add(-time.Second)
	raw, _ = json.Marshal(request)
	if _, err := readModuleStageInput(bytes.NewReader(raw)); err == nil {
		t.Fatal("expired lease reached publication")
	}
}
