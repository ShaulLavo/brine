package plan

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

// These fixtures prove deterministic orchestration intent, not native ownership.
func managedInitializedFixture(t *testing.T, frontend bool) Input {
	t.Helper()
	in, reserved, _ := managedPreparationFixture(t, frontend)
	engine := in.Desired.SpacetimeDB
	metadata := &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(strings.Repeat("a", 64)), RuntimeModuleHash: spec.RuntimeModuleHash(strings.Repeat("b", 64)), Canonicalizer: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("c", 64)}
	engine.Module.Metadata = metadata
	secret := func(reference string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: reference, Version: 1, SHA256: strings.Repeat("d", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	}
	runtime := data.EngineRuntime{BindingID: reserved.Database.BindingID, EpochID: reserved.Archive.EpochID, State: data.EngineInitialized, Image: data.SpacetimeDBImage, ManifestDigest: data.SpacetimeDBARM64Manifest, ImageID: strings.Repeat("e", 64), Version: "2.11.0", Runtime: engine.Runtime, HostPort: uint16(reserved.EnginePort), Database: data.NativeDatabaseIdentity(strings.Repeat("e", 64)), Publisher: data.NativePublisherIdentity(strings.Repeat("f", 64)), Names: []string{engine.Database}, Module: data.EngineModuleDefinition{Program: data.NativeProgramWireHash(metadata.RuntimeModuleHash), Wasm: data.WasmSHA256(metadata.WasmSHA256), Schema: data.EngineSchemaDefinition{Marker: engine.Module.Marker, Canonicalizer: metadata.Canonicalizer, SHA256: metadata.SchemaSHA256}}, Secrets: data.EngineSecrets{Publisher: secret(string(engine.Publisher)), SigningPrivate: secret(string(engine.SigningPrivate)), SigningPublic: secret(string(engine.SigningPublic)), AuthPolicy: secret(string(engine.Auth))}, MemoryMB: engine.Resources.MemoryMB, PIDsLimit: engine.Resources.PIDsLimit, PagePoolMB: engine.Resources.PagePoolMB, StopSeconds: engine.Resources.StopTimeoutSeconds, StartupSeconds: 60, CopyBytes: 1 << 20, CopyFiles: 128, UnitSHA256: strings.Repeat("f", 64)}
	if err := runtime.Validate(); err != nil {
		t.Fatal("invalid orchestration runtime", err)
	}
	in.Managed.Database = &reserved.Database
	in.Managed.Archive = &reserved.Archive
	in.Managed.Runtime = &runtime
	in.Managed.NativeState = target.Known("running")
	in.Managed.IngressProtected = target.Known(true)
	in.State.ManagedReservations = []target.ManagedPortReservation{{App: string(in.Desired.Name), BindingID: reserved.Database.BindingID, EnginePort: reserved.EnginePort}}
	if frontend {
		in.State.ManagedReservations[0].FrontendPort = reserved.Frontend.Port
	}
	return in
}

func TestManagedInitializedPlansPreserveTheCurrentEngine(t *testing.T) {
	for _, frontend := range []bool{false, true} {
		testManagedInitializedPlan(t, frontend)
	}
}
func testManagedInitializedPlan(t *testing.T, frontend bool) {
	t.Helper()
	in := managedInitializedFixture(t, frontend)
	p, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != Create || len(p.Conflicts) != 0 || p.Managed == nil || (p.Managed.Frontend != nil) != frontend {
		t.Fatalf("initialized components missing: %s %v", p.Kind, p.Conflicts)
	}
	for _, change := range p.Changes {
		if change.Kind == InstallManagedEngine || change.Kind == StartManagedEngine || change.Kind == PrepareManagedEngine {
			t.Fatal("running engine changed during client deployment")
		}
	}
	if p.Managed.RuntimeSHA256 == "" || p.Managed.EngineUnitHash != in.Managed.Runtime.UnitSHA256 {
		t.Fatal("current protected runtime not bound")
	}
	in.Desired.SpacetimeDB.Module.Metadata.SchemaSHA256 = strings.Repeat("d", 64)
	refused, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if refused.Kind != Conflict || len(refused.Changes) != 0 || !hasManagedConflict(refused, ManagedPublicationRequired) {
		t.Fatal("desired schema change became ordinary deploy")
	}
}

func TestManagedInitializedRefusesUnknownStateLoggingFenceAndServiceChanges(t *testing.T) {
	checks := []func(*Input){
		func(in *Input) { in.Managed.NativeState = target.Observation[string]{Status: target.Unknown} },
		func(in *Input) { in.Managed.IngressProtected = target.Known(false) },
		func(in *Input) { in.Managed.Fences = []data.QuiescenceFence{{State: data.FenceHeld}} },
		func(in *Input) { in.Desired.SpacetimeDB.Resources.MemoryMB++ },
	}
	for _, change := range checks {
		in := managedInitializedFixture(t, false)
		change(&in)
		planned, err := Build(in)
		if err != nil {
			t.Fatal(err)
		}
		if planned.Kind != Conflict || len(planned.Changes) != 0 {
			t.Fatal("unknown/mismatched runtime admitted")
		}
	}
}

func hasManagedConflict(p Plan, code ConflictCode) bool {
	for _, diagnostic := range p.Conflicts {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
