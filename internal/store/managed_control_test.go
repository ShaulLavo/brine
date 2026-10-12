//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

// This installs only the pending control tables for isolated CAS tests. It is
// not a schema19 migration/admission or native process/source acceptance proof.
func managedControlFixture(t *testing.T) (*Store, plan.Plan, ops.Operation) {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateManagedDesired(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = migrateManagedRoutes(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = migrateManagedFrontendPermits(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	a, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n[[allowed_registries]]\nhost=\"docker.io\"\nrepository_prefixes=[\"clockworklabs\"]\n")...)
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	a.SpacetimeDB.Image = spec.ImageReference(data.SpacetimeDBImage)
	a.SpacetimeDB.Runtime = data.RuntimeIdentity{UID: 1000, GID: 1000}
	desired, err := policy.Normalize(a, pol)
	if err != nil {
		t.Fatal(err)
	}
	request := engineReservation(t)
	request.App, request.PolicyHash = string(desired.Name), desired.PolicyHash
	request.Database.Name = desired.SpacetimeDB.Name
	request.Archive.Destination = desired.BackupDestinations[0]
	request.Archive.RemotePrefix, err = data.RemotePrefix(request.Archive.Destination.BasePrefix, request.Database.IncarnationID, request.Database.DatabaseID, request.Archive.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := s.ReserveEngine(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, s.dir, reserved.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	runtime := engineRuntimeFixture(reserved)
	runtime.ManifestDigest = data.SpacetimeDBARM64Manifest
	engine := desired.SpacetimeDB
	runtime.Runtime, runtime.MemoryMB, runtime.PIDsLimit = engine.Runtime, engine.Resources.MemoryMB, engine.Resources.PIDsLimit
	runtime.PagePoolMB, runtime.StopSeconds = engine.Resources.PagePoolMB, engine.Resources.StopTimeoutSeconds
	runtime.Secrets.AuthPolicy.Reference = string(engine.Auth)
	desired.SpacetimeDB.Module.Metadata = &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(runtime.Module.Wasm), RuntimeModuleHash: spec.RuntimeModuleHash(runtime.Module.Program), Canonicalizer: runtime.Module.Schema.Canonicalizer, SchemaSHA256: runtime.Module.Schema.SHA256}
	if err = s.RegisterEngineModule(ctx, runtime.BindingID, runtime.Module); err != nil {
		t.Fatal(err)
	}
	saveEngineAllocationFixture(t, s, reserved)
	fence, err := s.HoldDataFence(ctx, reserved.Database.DatabaseID, "control-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitEngineRuntime(ctx, runtime, fence.ID, "control-fixture"); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseDataFence(ctx, fence.ID, "control-fixture"); err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = desired
	in.Snapshot.Versions.Caddy = target.Known("2.10.2")
	image := plan.Image{Digest: strings.Split(runtime.Image, "@")[1], Platform: target.Platform{OS: "linux", Arch: in.Snapshot.Arch}, ManifestDigest: target.Known(data.SpacetimeDBARM64Manifest)}
	in.Managed = &plan.ManagedFacts{Database: &reserved.Database, Archive: &reserved.Archive, Runtime: &runtime, EngineImage: image, Fences: []data.QuiescenceFence{}, NativeState: target.Known("running"), LoggingPrerequisite: target.Known(true), LoggingRootSHA256: strings.Repeat("a", 64), LoggingConfigSHA256: strings.Repeat("b", 64)}
	p, err := plan.Build(in)
	if err != nil || p.Kind != plan.Create {
		t.Fatal("control plan", p.Conflicts, err)
	}
	if _, err = s.SavePlan(ctx, p, desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "requester", "control-fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Preflight, ops.Preparing} {
		if err = s.SetOperationState(ctx, op.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	return s, p, op
}

func TestManagedDesiredCASBindsPlanAndPreservesPrivatePermit(t *testing.T) {
	s, p, op := managedControlFixture(t)
	ctx := context.Background()
	if _, err := s.ReadManagedDesired(ctx, p.Managed.Database.BindingID); !errors.Is(err, ErrNotFound) {
		t.Fatal("absence", err)
	}
	wanted := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, wanted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, wanted); err != nil {
		t.Fatal("readback idempotence", err)
	}
	forged := wanted
	forged.Revision, forged.FrontendRunning = 2, true
	if _, err := s.CommitManagedDesired(ctx, op.ID, 1, forged); err == nil {
		t.Fatal("unplanned frontend granted")
	}
	forged = wanted
	forged.OperationID = "foreign"
	if _, err := s.CommitManagedDesired(ctx, "foreign", 0, forged); err == nil {
		t.Fatal("foreign job granted")
	}
	permit, err := s.ReadEnginePermit(ctx, wanted.BindingID)
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("private source permit overloaded", err)
	}
	row, err := s.ReadManagedDesired(ctx, wanted.BindingID)
	if err != nil || row != wanted {
		t.Fatal("failed CAS changed head", err)
	}
}
func TestManagedRouteReceiptNeedsCurrentDesiredRuntimeAndImmutableCAS(t *testing.T) {
	s, p, op := managedControlFixture(t)
	ctx := context.Background()
	wanted := ManagedRouteState{App: p.App, BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: op.ID, Revision: 1, Generation: 1, Present: true, FileHash: "sha256:" + strings.Repeat("a", 64), RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err := s.CommitManagedRouteState(ctx, 0, wanted); err == nil {
		t.Fatal("route without desired state granted")
	}
	desired := ManagedDesiredState{BindingID: wanted.BindingID, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, desired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitManagedRouteState(ctx, 0, wanted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitManagedRouteState(ctx, 0, wanted); err != nil {
		t.Fatal("route idempotence", err)
	}
	forged := wanted
	forged.Revision, forged.Present, forged.FileHash = 2, false, ""
	if _, err := s.CommitManagedRouteState(ctx, 1, forged); err == nil {
		t.Fatal("absence without withdrawal intent admitted")
	}
	forged = wanted
	forged.EpochID = data.ReplicaEpochID(strings.Repeat("9", 32))
	if _, err := s.CommitManagedRouteState(ctx, 0, forged); err == nil {
		t.Fatal("foreign epoch receipt admitted")
	}
	raw, _ := json.Marshal(wanted)
	if _, err := s.db.ExecContext(ctx, "UPDATE data_engine_route_states SET canonical=?", raw); err == nil {
		t.Fatal("immutable route proof mutable")
	}
	actual, err := s.ReadManagedRouteState(ctx, p.App)
	if err != nil || actual != wanted {
		t.Fatal("failed receipt changed route head", err)
	}
}

func TestManagedFailedCreateRestoresClientIntentWithoutStoppingNative(t *testing.T) {
	s, p, op := managedControlFixture(t)
	ctx := context.Background()
	wanted := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, wanted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreManagedDesired(ctx, op.ID, p); err == nil {
		t.Fatal("compensation outside rollback granted")
	}
	if err := s.SetOperationState(ctx, op.ID, ops.RollingBack); err != nil {
		t.Fatal(err)
	}
	restored, err := s.RestoreManagedDesired(ctx, op.ID, p)
	if err != nil || !restored.EngineRunning || restored.FrontendRunning || restored.IngressServing || restored.Revision != 2 {
		t.Fatal("native/client intent crossed", restored, err)
	}
	repeated, err := s.RestoreManagedDesired(ctx, op.ID, p)
	if err != nil || repeated != restored {
		t.Fatal("compensation idempotence", err)
	}
	original, err := readManagedDesiredRevision(ctx, s.db, wanted.BindingID, 1)
	if err != nil || original != wanted {
		t.Fatal("compensation mutated immutable intent", err)
	}
}

func TestManagedServingReleaseRequiresActualDesiredAndRouteReceipts(t *testing.T) {
	s, p, op := managedControlFixture(t)
	ctx := context.Background()
	unit := target.Unit{Name: "brine-engine-" + string(p.Managed.Database.BindingID) + ".service", Hash: "sha256:" + p.Managed.EngineUnitHash}
	release := Release{ID: op.ID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: []plan.SecretBinding{}, Units: []target.Unit{unit}, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, CaddyGeneration: 1, Managed: &ops.ManagedRelease{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, EngineUnit: unit, EnginePort: p.Managed.EnginePort}}
	if err := s.CommitRelease(ctx, p.App, release); err == nil {
		t.Fatal("release without actual component intent granted")
	}
	desired := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, desired); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitRelease(ctx, p.App, release); err == nil {
		t.Fatal("release without route receipt granted")
	}
	route := ManagedRouteState{App: p.App, BindingID: desired.BindingID, EpochID: desired.EpochID, RuntimeSHA256: desired.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: op.ID, Revision: 1, Generation: 1, Present: true, FileHash: release.CaddyFile.Hash, RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err := s.CommitManagedRouteState(ctx, 0, route); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitRelease(ctx, p.App, release); err == nil {
		t.Fatal("release committed before operation commit phase")
	}
	for _, state := range []ops.State{ops.Starting, ops.Checking, ops.Committing} {
		if err := s.SetOperationState(ctx, op.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CommitRelease(ctx, p.App, release); err != nil {
		t.Fatal("actual joined control receipts refused", err)
	}
}

func TestManagedRouteCASRejectsAConcurrentFenceOrRuntimeReplacement(t *testing.T) {
	for _, swap := range []bool{false, true} {
		testManagedRouteCASCurrentHead(t, swap)
	}
}
func testManagedRouteCASCurrentHead(t *testing.T, swap bool) {
	t.Helper()
	s, p, op := managedControlFixture(t)
	ctx := context.Background()
	desired := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: op.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, op.ID, 0, desired); err != nil {
		t.Fatal(err)
	}
	fence, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, "concurrent-data-operation")
	if err != nil {
		t.Fatal(err)
	}
	if swap {
		current, err := s.ReadEnginePermit(ctx, desired.BindingID)
		if err != nil {
			t.Fatal(err)
		}
		next := current.Runtime.Clone()
		next.Secrets.Publisher.Version++
		if err := s.CommitEngineRuntime(ctx, next, fence.ID, "concurrent-data-operation"); err != nil {
			t.Fatal(err)
		}
		if err := s.ReleaseDataFence(ctx, fence.ID, "concurrent-data-operation"); err != nil {
			t.Fatal(err)
		}
	}
	receipt := ManagedRouteState{App: p.App, BindingID: desired.BindingID, EpochID: desired.EpochID, RuntimeSHA256: desired.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: op.ID, Revision: 1, Generation: 1, Present: true, FileHash: "sha256:" + strings.Repeat("a", 64), RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err := s.CommitManagedRouteState(ctx, 0, receipt); err == nil {
		t.Fatal("historical desired stamp granted present route under changed fence/head", swap)
	}
	if _, err := s.ReadManagedRouteState(ctx, p.App); !errors.Is(err, ErrNotFound) {
		t.Fatal("refusal mutated route head", err)
	}
}
