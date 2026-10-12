package apply

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedRuntimeFake struct {
	rig     *rig
	calls   []string
	failure string
}

func (f *managedRuntimeFake) call(name string) error {
	f.calls = append(f.calls, name)
	if f.failure == name {
		return injected
	}
	return nil
}
func (f *managedRuntimeFake) StageEngine(context.Context, plan.Plan, ops.Release) error {
	return f.call("engine_stage")
}
func (f *managedRuntimeFake) InstallEngine(context.Context, plan.Plan, ops.Release) error {
	return f.call("engine_install")
}
func (f *managedRuntimeFake) VerifyEngineUnit(context.Context, plan.Plan) error {
	return f.call("engine_verify")
}
func (f *managedRuntimeFake) EngineReady(context.Context, plan.Plan) error {
	return f.call("engine_ready")
}
func (f *managedRuntimeFake) BeforeEngineStart(context.Context, plan.Plan) error {
	return f.call("engine_start_permit")
}
func (f *managedRuntimeFake) BindFrontend(context.Context, string, plan.Plan, string) error {
	return f.call("frontend_bind")
}
func (f *managedRuntimeFake) RestoreFrontendPermit(context.Context, string, string) error {
	return f.call("frontend_restore_permit")
}
func (f *managedRuntimeFake) BeforeFrontendStart(context.Context, string, plan.Plan, policy.Desired, string) error {
	return f.call("frontend_start_permit")
}
func (f *managedRuntimeFake) BeforeIngress(context.Context, string, plan.Plan, policy.Desired) error {
	return f.call("route_permit")
}

func managedInitializedRig(t *testing.T, frontend bool) *rig {
	t.Helper()
	r := managedPreparationRig(t, frontend)
	in := r.facts.Input
	reserved := r.plan.Managed
	engine := in.Desired.SpacetimeDB
	metadata := &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(strings.Repeat("a", 64)), RuntimeModuleHash: spec.RuntimeModuleHash(strings.Repeat("b", 64)), Canonicalizer: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("c", 64)}
	engine.Module.Metadata = metadata
	secret := func(reference string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: reference, Version: 1, SHA256: strings.Repeat("d", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	}
	runtime := data.EngineRuntime{BindingID: reserved.Database.BindingID, EpochID: reserved.Archive.EpochID, State: data.EngineInitialized, Image: data.SpacetimeDBImage, ManifestDigest: data.SpacetimeDBARM64Manifest, ImageID: strings.Repeat("e", 64), Version: "2.11.0", Runtime: engine.Runtime, HostPort: uint16(reserved.EnginePort), Database: data.NativeDatabaseIdentity(strings.Repeat("e", 64)), Publisher: data.NativePublisherIdentity(strings.Repeat("f", 64)), Names: []string{engine.Database}, Module: data.EngineModuleDefinition{Program: data.NativeProgramWireHash(metadata.RuntimeModuleHash), Wasm: data.WasmSHA256(metadata.WasmSHA256), Schema: data.EngineSchemaDefinition{Marker: engine.Module.Marker, Canonicalizer: metadata.Canonicalizer, SHA256: metadata.SchemaSHA256}}, Secrets: data.EngineSecrets{Publisher: secret(string(engine.Publisher)), SigningPrivate: secret(string(engine.SigningPrivate)), SigningPublic: secret(string(engine.SigningPublic)), AuthPolicy: secret(string(engine.Auth))}, MemoryMB: engine.Resources.MemoryMB, PIDsLimit: engine.Resources.PIDsLimit, PagePoolMB: engine.Resources.PagePoolMB, StopSeconds: engine.Resources.StopTimeoutSeconds, StartupSeconds: 60, CopyBytes: 1 << 20, CopyFiles: 128, UnitSHA256: strings.Repeat("f", 64)}
	in.Managed.Database = &reserved.Database
	in.Managed.Archive = &reserved.Archive
	in.Managed.Runtime = &runtime
	in.Managed.NativeState = target.Known("running")
	in.Managed.IngressProtected = target.Known(true)
	in.State.ManagedReservations = []target.ManagedPortReservation{{App: string(in.Desired.Name), BindingID: reserved.Database.BindingID, EnginePort: reserved.EnginePort}}
	if frontend {
		in.State.ManagedReservations[0].FrontendPort = reserved.Frontend.Port
	}
	planned, err := plan.Build(in)
	if err != nil || planned.Kind != plan.Create {
		t.Fatalf("initialized orchestration %v %v", planned.Conflicts, err)
	}
	r.plan = planned
	r.desired = in.Desired
	r.facts.Input = in
	return r
}

func TestManagedEngineOnlyReleaseUsesCurrentRuntimeWithoutNativeMutation(t *testing.T) {
	r := managedInitializedRig(t, false)
	runtime := &managedRuntimeFake{rig: r}
	r.executor.Managed = runtime
	if err := r.executor.Run(context.Background(), "op", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !r.committed || r.release.Managed == nil || r.release.Managed.Frontend != nil || len(r.release.Units) != 1 || r.release.Managed.RuntimeSHA256 != r.plan.Managed.RuntimeSHA256 {
		t.Fatal("engine-only release omitted its current engine")
	}
	for _, call := range runtime.calls {
		if call == "engine_start_permit" || call == "engine_install" || call == "frontend_bind" {
			t.Fatal("running engine-only release changed components", runtime.calls)
		}
	}
}

func TestManagedOptionalFrontendLifecyclePreservesRunningEngine(t *testing.T) {
	r := managedInitializedRig(t, true)
	runtime := &managedRuntimeFake{rig: r}
	r.executor.Managed = runtime
	if err := r.executor.Run(context.Background(), "op", r.plan, r.desired); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !r.committed || r.release.Managed == nil || r.release.Managed.Frontend == nil || len(r.release.Units) != 2 || r.release.Managed.RuntimeSHA256 != r.plan.Managed.RuntimeSHA256 {
		t.Fatal("optional frontend release components differ")
	}
	seen := map[string]bool{}
	for _, call := range runtime.calls {
		seen[call] = true
	}
	if !seen["frontend_bind"] || !seen["frontend_start_permit"] || !seen["route_permit"] || seen["engine_start_permit"] || seen["engine_install"] {
		t.Fatal("frontend/client permits were mixed with engine start", runtime.calls)
	}
}
