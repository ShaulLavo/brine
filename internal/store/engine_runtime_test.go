//go:build linux

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func engineRuntimeFixture(reserved ReservedEngine) data.EngineRuntime {
	secret := func(reference string) data.EngineSecretVersion {
		return data.EngineSecretVersion{Reference: reference, Version: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	}
	return data.EngineRuntime{BindingID: reserved.Database.BindingID, EpochID: reserved.Archive.EpochID, State: data.EngineInitialized, Image: data.SpacetimeDBImage, ManifestDigest: data.SpacetimeDBAMD64Manifest, ImageID: strings.Repeat("a", 64), Version: "2.11.0", Runtime: data.RuntimeIdentity{UID: 1000, GID: 1000}, HostPort: 23111, Database: data.NativeDatabaseIdentity(strings.Repeat("e", 64)), Publisher: data.NativePublisherIdentity(strings.Repeat("f", 64)), Names: []string{"game"}, Module: data.EngineModuleDefinition{Program: data.NativeProgramWireHash(strings.Repeat("b", 64)), Wasm: data.WasmSHA256(strings.Repeat("c", 64)), Schema: data.EngineSchemaDefinition{Marker: "game-v1", Canonicalizer: "spacetimedb-v10-expanded-v2", SHA256: strings.Repeat("d", 64)}}, Secrets: data.EngineSecrets{Publisher: secret("publisher"), SigningPrivate: secret("signing-private"), SigningPublic: secret("signing-public"), AuthPolicy: secret("auth-policy")}, MemoryMB: 256, PIDsLimit: 128, PagePoolMB: 64, StopSeconds: 10, StartupSeconds: 60, CopyBytes: 1 << 20, CopyFiles: 128, UnitSHA256: strings.Repeat("a", 64)}
}

func saveEngineAllocationFixture(t *testing.T, s *Store, reserved ReservedEngine) {
	t.Helper()
	root := &data.RootEvidence{Root: reserved.Database.Root, Device: 1, Inode: 2, Filesystem: "xfs", POSIXLocks: true, DurableRename: true, FreeBytes: 1 << 30, FreeInodes: 100, ObservedAt: time.Now().UTC()}
	mapping := &data.MappingEvidence{Runtime: data.RuntimeIdentity{UID: 1000, GID: 1000}, RunnerUID: 1000, RunnerGID: 1000, Image: data.MappingProbeImage, Root: root.Root, Device: root.Device, KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: root.ObservedAt}
	receipt := data.AllocationReceipt{RootProof: root, MappingProof: mapping, DatabaseID: reserved.Database.DatabaseID, IncarnationID: reserved.Database.IncarnationID, RootDevice: 1, RootInode: 2, DirectoryDevice: 1, DirectoryInode: 3, AllocatedAt: root.ObservedAt}
	if err := s.SaveEngineAllocation(context.Background(), reserved.Database.BindingID, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestEngineRuntimeCommitNeedsOwnedFenceAndKeepsStartupFenced(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveEngine(ctx, engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, s.dir, reserved.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	runtime := engineRuntimeFixture(reserved)
	if err = s.RegisterEngineModule(ctx, reserved.Database.BindingID, runtime.Module); err != nil {
		t.Fatal(err)
	}
	saveEngineAllocationFixture(t, s, reserved)
	if err = s.CommitEngineRuntime(ctx, runtime, "", "fixture"); err == nil {
		t.Fatal("runtime committed without a durable fence")
	}
	fence, err := s.HoldDataFence(ctx, reserved.Database.DatabaseID, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CommitEngineRuntime(ctx, runtime, fence.ID, "fixture"); err != nil {
		t.Fatal(err)
	}
	permit, err := s.ReadEnginePermit(ctx, reserved.Database.BindingID)
	if err != nil || permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("committed runtime released the fence", err)
	}
	if err = s.ReleaseDataFence(ctx, fence.ID, "fixture"); err != nil {
		t.Fatal(err)
	}
	permit, err = s.ReadEnginePermit(ctx, reserved.Database.BindingID)
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("matching initialized ownership refused", err)
	}
	permit.Runtime.Secrets.Publisher.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("expired publisher metadata admitted startup")
	}
}

func TestEngineModuleRegistryAllowsLogicChangeWithoutMarkerRemapping(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveEngine(ctx, engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, s.dir, reserved.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	first := engineRuntimeFixture(reserved).Module
	if err = s.RegisterEngineModule(ctx, reserved.Database.BindingID, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Program = data.NativeProgramWireHash(strings.Repeat("e", 64))
	second.Wasm = data.WasmSHA256(strings.Repeat("f", 64))
	if err = s.RegisterEngineModule(ctx, reserved.Database.BindingID, second); err != nil {
		t.Fatal("logic change required a new schema marker", err)
	}
	second.Schema.SHA256 = strings.Repeat("a", 64)
	if err = s.RegisterEngineModule(ctx, reserved.Database.BindingID, second); err == nil {
		t.Fatal("immutable schema marker was remapped")
	}
}
