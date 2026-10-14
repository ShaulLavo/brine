//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type emptyManagedContainers struct{}

func (emptyManagedContainers) Execute(ctx context.Context, command localexec.Command) (localexec.Result, error) {
	if command.Path != "/usr/bin/podman" || command.Mutation {
		return localexec.Result{}, fmt.Errorf("unexpected managed observation")
	}
	return localexec.Result{}, ctx.Err()
}

// Actual schema19/store/private-file/apply operation. Podman/systemd and mapping
// observations are stand-ins; this does not claim an engine start or VPS deploy.
func TestManagedPreparationActualStoreAndPrivateFilesRemainUnpublished(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(cwd, ".managed-preparation-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	stateRoot, root := filepath.Join(home, "state"), filepath.Join(home, "data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := store.Open(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "/srv/brine-data", root, 1))
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	app.SpacetimeDB.Image = spec.ImageReference(data.SpacetimeDBImage)
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "/srv/brine-data", root, 1) + "\n[[allowed_registries]]\nhost=\"ghcr.io\"\nrepository_prefixes=[\"shaullavo\"]\n")
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../target/testdata/ready-arm64.json")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := target.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Versions.Caddy = target.Known("2.10.2")
	image := plan.Image{Digest: strings.Split(data.SpacetimeDBImage, "@")[1], Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(data.SpacetimeDBARM64Manifest)}
	in := plan.Input{Desired: desired, Snapshot: snapshot, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}, Image: image, Managed: &plan.ManagedFacts{EngineImage: image, Fences: []data.QuiescenceFence{}}}
	proposal, err := managedProposal(in)
	if err != nil {
		t.Fatal(err)
	}
	proposal.EnginePort = 20000
	rootProof, err := data.ProbeRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := plan.BuildManagedPreparation(in, proposal, rootProof)
	if err != nil || planned.Kind != plan.Create {
		t.Fatal("managed preparation", err, planned.Conflicts)
	}
	if _, err := state.SavePlan(ctx, planned, desired); err != nil {
		t.Fatal(err)
	}
	operation, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: planned.Hash}, "fixture", "managed-prepare")
	if err != nil {
		t.Fatal(err)
	}
	uidValue, gidValue := int64(os.Geteuid()), int64(os.Getegid())
	if uidValue < 0 || uidValue > math.MaxUint32 || gidValue < 0 || gidValue > math.MaxUint32 {
		t.Fatal("fixture identity invalid")
		return
	}
	uid, gid := uint32(uidValue), uint32(gidValue)
	session, err := localexec.NewSession(emptyManagedContainers{}, uid, home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	manager := &systemd.Fake{JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }, ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}}
	observer := managedEngineObserver{state: state, stateRoot: stateRoot, home: home, session: session, units: manager}
	pull := &preparationPullRunner{}
	preparation := DataPreparation{State: state, StateRoot: stateRoot, Home: home, Runner: pull, managedObserver: &observer, ProbeRoot: data.ProbeRoot, ProbeMapping: func(_ context.Context, _ localexec.Runner, r data.RootEvidence, identity data.RuntimeIdentity) (data.MappingEvidence, error) {
		return data.MappingEvidence{Runtime: identity, RunnerUID: uid, RunnerGID: gid, Image: data.MappingProbeImage, Root: r.Root, Device: r.Device, KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: time.Now().UTC()}, nil
	}}
	engine := apply.Executor{Journal: state, Plans: state, ManagedPreparation: preparation}
	if err := engine.Run(ctx, operation.ID, planned, desired); err != nil {
		t.Fatal(err)
	}
	ready, err := preparation.ManagedPrepared(ctx, operation.ID, planned, desired)
	if err != nil || !ready {
		t.Fatal("actual prepared readback", err)
	}
	entries, err := os.ReadDir(planned.Managed.Database.SourcePath())
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatal("preparation initialized native data", entries, err)
	}
	if _, err := state.CurrentRelease(ctx, planned.App); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("preparation created serving release", err)
	}
	if _, err := state.ReadEnginePermit(ctx, planned.Managed.Database.BindingID); err == nil {
		t.Fatal("preparation created native runtime")
	}
	if _, err := state.ReadManagedFrontendPermit(ctx, planned.App); err == nil {
		t.Fatal("preparation created frontend permit")
	}
	inventory, err := state.InventoryState(ctx)
	if err != nil || len(inventory.ManagedReservations) != 1 || inventory.ManagedReservations[0].BindingID != planned.Managed.Database.BindingID {
		t.Fatal("prepared ports not projected", err)
	}
}
