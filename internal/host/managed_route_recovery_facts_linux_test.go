//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedFactsRecoveryRuntime struct {
	apply.ManagedRuntime
	running bool
	cached  managedRuntime
}

func (r *managedFactsRecoveryRuntime) CompletedManaged(context.Context, string, plan.Plan, policy.Desired) error {
	if !r.running {
		return fmt.Errorf("frontend stopped")
	}
	return nil
}
func (r *managedFactsRecoveryRuntime) StoppedManagedFrontend(ctx context.Context, _ string, p plan.Plan, d policy.Desired) error {
	if r.running {
		return fmt.Errorf("frontend running")
	}
	return r.cached.stoppedFrontendImage(ctx, p, d)
}
func (*managedFactsRecoveryRuntime) CheckManagedIngress(context.Context, caddy.ManagedIngressClaim) error {
	return nil
}

type managedFactsRecoveryRoutes struct {
	apply.Routes
	expected caddy.State
}

func (r managedFactsRecoveryRoutes) InspectManagedLoaded(_ context.Context, current caddy.State, p plan.Plan, _ policy.Desired) error {
	if current.Generation != r.expected.Generation || current.Files[p.App+".caddy"] != r.expected.Files[p.App+".caddy"] || len(current.Sites) == 0 {
		return fmt.Errorf("applied route missing from actual service facts")
	}
	return nil
}

type managedFactsImages struct{ frontend, engine plan.Image }

func (r managedFactsImages) Resolve(_ context.Context, reference spec.ImageReference, _ target.Platform) (plan.Image, error) {
	if string(reference) == data.SpacetimeDBImage {
		return r.engine, nil
	}
	return r.frontend, nil
}
func managedFactsRecoveryFixture(t *testing.T, configure ...func(*data.EngineRuntime)) (*deployRig, plan.Plan, ops.Operation, caddy.State) {
	t.Helper()
	return managedFactsRecoveryReceiptFixture(t, true, configure...)
}

func managedFactsRecoveryReceiptFixture(t *testing.T, recorded bool, configure ...func(*data.EngineRuntime)) (*deployRig, plan.Plan, ops.Operation, caddy.State) {
	t.Helper()
	stateRoot := t.TempDir()
	r := newDeployRigAt(t, stateRoot)
	ctx := context.Background()
	permit, _ := engineStartupFixture(t)
	runtime, binding := permit.Runtime, permit.Reserved.Database
	binding.Name = "main"
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("/srv/brine-data"), []byte(binding.Root))
	raw = append([]byte("image=\"registry.example.com:5000/team/frontend@sha256:"+strings.Repeat("a", 64)+"\"\ncontainer_port=8080\nresources={memory_mb=128,pids_limit=128}\n"), raw...)
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	app.SpacetimeDB.Image = spec.ImageReference(data.SpacetimeDBImage)
	policyRaw, err := os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	policyRaw = bytes.ReplaceAll(policyRaw, []byte("/srv/brine-data"), []byte(binding.Root))
	policyRaw = append(policyRaw, []byte("\n[[allowed_registries]]\nhost=\"ghcr.io\"\nrepository_prefixes=[\"shaullavo\"]\n")...)
	pol, err := policy.Parse(policyRaw)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	runtime.HostPort = 20000
	runtime.ManifestDigest = data.SpacetimeDBARM64Manifest
	engine := desired.SpacetimeDB
	runtime.MemoryMB, runtime.PIDsLimit, runtime.StopSeconds = engine.Resources.MemoryMB, engine.Resources.PIDsLimit, engine.Resources.StopTimeoutSeconds
	runtime.Secrets.AuthPolicy.Reference = string(engine.Auth)
	if len(configure) != 0 {
		configure[0](&runtime)
	}
	desired.SpacetimeDB.Module.Metadata = &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(runtime.Module.Wasm), RuntimeModuleHash: spec.RuntimeModuleHash(runtime.Module.Program), Canonicalizer: runtime.Module.Schema.Canonicalizer, SchemaSHA256: runtime.Module.Schema.SHA256}
	destination := desired.BackupDestinations[0]
	prefix, err := data.RemotePrefix(destination.BasePrefix, binding.IncarnationID, binding.DatabaseID, runtime.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := r.store.ReserveEngine(ctx, store.EngineReservation{App: string(desired.Name), PolicyHash: desired.PolicyHash, Database: binding, Archive: data.ArchiveBinding{BindingID: binding.BindingID, DatabaseID: binding.DatabaseID, EpochID: runtime.EpochID, Destination: destination, RemotePrefix: prefix}})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.PrepareEngineLocks(ctx, stateRoot, binding.BindingID); err != nil {
		t.Fatal(err)
	}
	if err := r.store.RegisterEngineModule(ctx, binding.BindingID, runtime.Module); err != nil {
		t.Fatal(err)
	}
	root := &data.RootEvidence{Root: binding.Root, Device: 1, Inode: 2, Filesystem: "xfs", POSIXLocks: true, DurableRename: true, FreeBytes: 1 << 30, FreeInodes: 100, ObservedAt: time.Now().UTC()}
	mapping := &data.MappingEvidence{Runtime: runtime.Runtime, RunnerUID: 1000, RunnerGID: 1000, Image: data.MappingProbeImage, Root: root.Root, Device: root.Device, KeepID: true, PrivateModes: true, HostReadWrite: true, ContainerReadWrite: true, ObservedAt: root.ObservedAt}
	if err := r.store.SaveEngineAllocation(ctx, binding.BindingID, data.AllocationReceipt{RootProof: root, MappingProof: mapping, DatabaseID: binding.DatabaseID, IncarnationID: binding.IncarnationID, RootDevice: 1, RootInode: 2, DirectoryDevice: 1, DirectoryInode: 3, AllocatedAt: root.ObservedAt}); err != nil {
		t.Fatal(err)
	}
	fence, err := r.store.HoldDataFence(ctx, binding.DatabaseID, "fixture-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.CommitEngineRuntime(ctx, runtime, fence.ID, "fixture-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := r.store.ReleaseDataFence(ctx, fence.ID, "fixture-runtime"); err != nil {
		t.Fatal(err)
	}
	engineImage := plan.Image{Digest: strings.Split(runtime.Image, "@")[1], Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known(runtime.ManifestDigest)}
	frontend := plan.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: engineImage.Platform, ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64))}
	snap := r.inventory.snapshot
	snap.Versions.Caddy = target.Known("2.10.2")
	input := plan.Input{Desired: desired, Snapshot: snap, State: plan.BrineState{Target: snap.Identity, Releases: []plan.CurrentRelease{}}, Image: frontend, Managed: &plan.ManagedFacts{Database: &reserved.Database, Archive: &reserved.Archive, Runtime: &runtime, EngineImage: engineImage, Fences: []data.QuiescenceFence{}, NativeState: target.Known("running"), OwnedNativeState: target.Known("running"), LoggingPrerequisite: target.Known(true), LoggingRootSHA256: strings.Repeat("a", 64), LoggingConfigSHA256: strings.Repeat("b", 64)}}
	p, err := plan.Build(input)
	if err != nil || p.Kind != plan.Create {
		t.Fatal(err, p.Conflicts)
	}
	if _, err := r.store.SavePlan(ctx, p, desired); err != nil {
		t.Fatal(err)
	}
	source, _, err := r.store.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, r.service.Requester, "managed-route-original")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []ops.State{ops.Preflight, ops.Preparing} {
		if err := r.store.SetOperationState(ctx, source.ID, phase); err != nil {
			t.Fatal(err)
		}
	}
	wanted := store.ManagedDesiredState{BindingID: binding.BindingID, EpochID: runtime.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: source.ID, EngineRunning: true, FrontendRunning: true, IngressServing: true}
	if _, err := r.store.CommitManagedDesired(ctx, source.ID, 0, wanted); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetOperationState(ctx, source.ID, ops.Starting); err != nil {
		t.Fatal(err)
	}
	unit, err := quadlet.Render(desired, p, *frontend.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.BindManagedFrontendPermit(ctx, source.ID, p.Hash, unit.Hash()); err != nil {
		t.Fatal(err)
	}
	site, err := caddy.NewManagedSite(desired, pol, *p.Managed)
	if err != nil {
		t.Fatal(err)
	}
	routeBytes, err := caddy.Render(site)
	if err != nil {
		t.Fatal(err)
	}
	file := target.CaddyFile{Name: p.App + ".caddy", Hash: fmt.Sprintf("sha256:%x", sha256.Sum256(routeBytes))}
	route := store.ManagedRouteState{App: p.App, BindingID: binding.BindingID, EpochID: runtime.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: source.ID, Revision: 1, Generation: 1, Present: true, FileHash: file.Hash, RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if recorded {
		if _, err := r.store.CommitManagedRouteState(ctx, 0, route); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.store.SetOperationState(ctx, source.ID, ops.Checking); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"preflight", "managed_desired", "engine_stage", "frontend_pull", "frontend_verify", "frontend_secrets", "frontend_stage", "engine_check", "frontend_install", "frontend_reload", "frontend_bind", "frontend_start", "frontend_check", "managed_route"} {
		appendHostRecoveryStep(t, r.store, source.ID, step, "intent")
		appendHostRecoveryStep(t, r.store, source.ID, step, "completed")
	}
	appendHostRecoveryStep(t, r.store, source.ID, "managed_route_receipt", "intent")
	if recorded {
		appendHostRecoveryStep(t, r.store, source.ID, "managed_route_receipt", "completed")
		appendHostRecoveryStep(t, r.store, source.ID, "managed_route_check", "intent")
		appendHostRecoveryStep(t, r.store, source.ID, "managed_route_check", "unknown")
	} else {
		appendHostRecoveryStep(t, r.store, source.ID, "managed_route_receipt", "unknown")
	}
	if err := r.store.SetOperationState(ctx, source.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	source.State = ops.RecoveryRequired
	snap.Apps = target.Known([]target.App{{Name: p.App, Image: target.Known(target.Image{Digest: frontend.Digest, Platform: frontend.Platform}), AllocatedHostPort: target.Known(p.HostPort), QuadletUnits: target.Known([]target.Unit{{Name: unit.Name(), Hash: unit.Hash()}})}})
	snap.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 1, Files: []target.CaddyFile{file}})
	r.inventory.snapshot = snap
	r.policy.p = pol
	r.service.Policy = r.policy
	r.service.Images = managedFactsImages{frontend: frontend, engine: engineImage}
	return r, p, source, caddy.State{Generation: 1, Files: map[string]string{file.Name: file.Hash}, Sites: map[string]caddy.Site{file.Name: site}}
}
func appendHostRecoveryStep(t *testing.T, state *store.Store, id, step, outcome string) {
	t.Helper()
	raw, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: outcome})
	if _, err := state.AppendEvent(context.Background(), id, ops.Event{Kind: "step", Payload: raw}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRecoveryFactsObserveMissingReceiptWithoutWritingIt(t *testing.T) {
	r, p, source, expected := managedFactsRecoveryReceiptFixture(t, false)
	ctx := t.Context()
	_, desired, err := r.store.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	reader := operationFacts{service: r.service, desired: desired, recoveryPlan: p, recoveryOperation: source}
	facts, err := reader.Read(ctx)
	if err != nil || facts.Routing.Generation != expected.Generation || facts.Routing.Files[p.App+".caddy"] != expected.Files[p.App+".caddy"] || facts.Input.State.Generation != *p.ObservedGeneration.Value+1 {
		t.Fatal("actual unrecorded route was not observed", err, facts.Routing)
	}
	if _, err := r.store.ReadManagedRouteState(ctx, p.App); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("facts reader invented a route receipt", err)
	}
	r.inventory.snapshot.CaddyConfig.Value.Files[0].Hash = "sha256:" + strings.Repeat("f", 64)
	if _, err := reader.Read(ctx); err == nil {
		t.Fatal("foreign unrecorded route accepted")
	}
}

func TestManagedResolutionUsesPublicServiceFactsForAppliedUncommittedRoute(t *testing.T) {
	for _, kind := range []string{"failed-known-cache", "failed-foreign-image", "failed-wrong-platform", "failed-foreign-container", "running-unknown-live-image", "changed-owned-unit"} {
		t.Run(kind, func(t *testing.T) { testManagedResolutionPublicFacts(t, kind) })
	}
}
func testManagedResolutionPublicFacts(t *testing.T, kind string) {
	t.Helper()
	r, p, source, expected := managedFactsRecoveryFixture(t)
	ctx := context.Background()
	_, desired, err := r.store.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := r.service.facts(ctx, App(desired))
	if err != nil || len(ordinary.Routing.Files) != 0 || ordinary.Input.Snapshot.CaddyConfig.Value == nil || len(ordinary.Input.Snapshot.CaddyConfig.Value.Files) != 1 {
		t.Fatal("ordinary planning adopted an uncommitted route", err, ordinary.Routing)
	}
	cache := newManagedStoppedImageRunner(p, desired)
	session, err := localexec.NewSession(cache, 1000, t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &managedFactsRecoveryRuntime{cached: managedRuntime{observer: managedEngineObserver{session: session}}}
	// Failed units intentionally have no live image or allocated-port facts.
	(*r.inventory.snapshot.Apps.Value)[0].Image = target.Observation[target.Image]{Status: target.Unknown}
	(*r.inventory.snapshot.Apps.Value)[0].AllocatedHostPort = target.Observation[target.Port]{Status: target.Unknown}
	switch kind {
	case "failed-foreign-image":
		cache.image = strings.ReplaceAll(cache.image, string(desired.Image), "registry.example.com:5000/foreign@sha256:"+strings.Repeat("f", 64))
	case "failed-wrong-platform":
		cache.image = strings.ReplaceAll(cache.image, "arm64", "amd64")
	case "failed-foreign-container":
		cache.container = `[{"Name":"foreign","Image":"foreign","State":{"Running":false,"Status":"exited"},"Config":{"Labels":{}}}]`
	case "running-unknown-live-image":
		runtime.running = true
	case "changed-owned-unit":
		(*r.inventory.snapshot.Apps.Value)[0].QuadletUnits.Value = &[]target.Unit{{Name: p.App + ".container", Hash: "sha256:" + strings.Repeat("f", 64)}}
	}
	starts := 0
	manager := &systemd.Fake{IsActiveFunc: func(context.Context, systemd.Unit) (bool, error) { return runtime.running, nil }, StartFunc: func(ctx context.Context, unit systemd.Unit) error {
		starts++
		if unit.String() != p.App+".service" {
			t.Fatal("unexpected component start", unit)
		}
		permit, err := r.store.ReadManagedFrontendPermit(ctx, p.App)
		if err != nil || !permit.Pending || permit.PendingOperationID == source.ID {
			t.Fatal("normal start lacked resolver grant", err)
		}
		runtime.running = true
		(*r.inventory.snapshot.Apps.Value)[0].Image = target.Known(target.Image{Digest: p.Image.Digest, Platform: p.Image.Platform})
		(*r.inventory.snapshot.Apps.Value)[0].AllocatedHostPort = target.Known(p.HostPort)
		return nil
	}}
	engine := apply.Executor{Journal: r.store, Releases: releases{r.store}, Managed: runtime, Routes: managedFactsRecoveryRoutes{expected: expected}, Systemd: manager, Health: r.health, Podman: &podman.Fake{SecretExistsFunc: func(context.Context, podman.Name) (bool, error) { return true, nil }}}
	reconciler := newReconciler(r.service, engine, manager)
	runner := jobs.Runner{Store: r.store, Executor: Executor{Service: r.service, Engine: engine}, Reconciler: runnerReconciler{reconciler}}
	accepted, err := (jobs.Service{Store: r.store, Launcher: r.launcher, Requester: r.service.Requester}).Resolve(ctx, source.ID, "public-managed-route-resolution")
	if err != nil {
		t.Fatal(err)
	}
	runErr := runner.Run(ctx, accepted.OperationID)
	if kind != "failed-known-cache" {
		if runErr == nil || starts != 0 || r.health.calls != 0 {
			t.Fatal("changed or live-unknown image proof reached effects", runErr, starts, r.health.calls)
		}
		if _, err := r.store.CurrentRelease(ctx, p.App); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("refusal committed release", err)
		}
		return
	}
	if runErr != nil {
		t.Fatal("actual public service facts refused applied route", runErr)
	}
	completed, err := r.store.GetOperation(ctx, accepted.OperationID)
	if err != nil || completed.State != ops.Succeeded || starts != 1 || r.health.calls != 2 {
		t.Fatal("public resolution did not complete", err, completed.State, starts, r.health.calls)
	}
	original, err := r.store.GetOperation(ctx, source.ID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("original receipt changed", err)
	}
	committed, err := r.store.CurrentRelease(ctx, p.App)
	if err != nil || committed.ID != source.ID || committed.CaddyFile.Hash != expected.Files[p.App+".caddy"] {
		t.Fatal("wrong applied route committed", err)
	}
}

func TestManagedRecoveryFactsRefuseChangedRouteProofWithoutEffects(t *testing.T) {
	for _, name := range []string{"missing-file", "changed-file", "generation", "unknown-route", "policy", "state-generation", "foreign-owner", "fence"} {
		t.Run(name, func(t *testing.T) {
			r, p, source, _ := managedFactsRecoveryFixture(t)
			ctx := context.Background()
			_, desired, err := r.store.LoadPlan(ctx, p.Hash)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing-file":
				r.inventory.snapshot.CaddyConfig.Value.Files = nil
			case "changed-file":
				r.inventory.snapshot.CaddyConfig.Value.Files[0].Hash = "sha256:" + strings.Repeat("f", 64)
			case "generation":
				r.inventory.snapshot.CaddyConfig.Value.Generation++
			case "unknown-route":
				r.inventory.snapshot.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unknown}
			case "policy":
				r.policy.p = newDeployRig(t).policy.p
			case "state-generation":
				r.inventory.generationOffset = 1
			case "foreign-owner":
				source.ID = "foreign-owner"
			case "fence":
				if _, err := r.store.HoldDataFence(ctx, p.Managed.Database.DatabaseID, "foreign-data-task"); err != nil {
					t.Fatal(err)
				}
			}
			reader := operationFacts{service: r.service, desired: desired, recoveryPlan: p, recoveryOperation: source}
			if _, err := reader.Read(ctx); err == nil {
				t.Fatal("changed protected route proof accepted", name)
			}
			if r.units.installs != 0 || r.pulls != 0 || r.health.calls != 0 {
				t.Fatal("facts reader performed effects")
			}
		})
	}
}
func TestManagedRecoveryRouteInventoryRejectsDuplicateOwnership(t *testing.T) {
	r, p, _, _ := managedFactsRecoveryFixture(t)
	route, err := r.store.ReadManagedRouteState(context.Background(), p.App)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := r.inventory.snapshot
	snapshot.CaddyConfig.Value.Files = append(snapshot.CaddyConfig.Value.Files, snapshot.CaddyConfig.Value.Files[0])
	if managedRecoveryRouteObserved(snapshot, route) {
		t.Fatal("duplicate route file ownership accepted")
	}
}

func TestManagedRecoveryFactsPreserveUnrelatedObservedCommittedRoutes(t *testing.T) {
	r, p, source, expected := managedFactsRecoveryFixture(t)
	ctx := context.Background()
	_, desired, err := r.store.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := r.service.facts(ctx, App(desired))
	if err != nil {
		t.Fatal(err)
	}
	const other = "other.caddy"
	otherHash := "sha256:" + strings.Repeat("e", 64)
	otherSite := expected.Sites[p.App+".caddy"]
	facts.Input.State.Releases = append(facts.Input.State.Releases, plan.CurrentRelease{App: "other"})
	facts.Routing.Files[other] = otherHash
	facts.Routing.Sites[other] = otherSite
	reader := operationFacts{service: r.service, desired: desired, recoveryPlan: p, recoveryOperation: source}
	recovered, err := reader.managedRecoveryRoutes(ctx, facts)
	if err != nil || recovered.Routing.Files[other] != otherHash || len(recovered.Routing.Files) != 2 || len(recovered.Routing.Sites) != 2 {
		t.Fatal("recovery replaced unrelated committed routing", err, recovered.Routing)
	}
	otherBytes, err := caddy.Render(recovered.Routing.Sites[other])
	if err != nil {
		t.Fatal(err)
	}
	expectedBytes, err := caddy.Render(otherSite)
	if err != nil || !bytes.Equal(otherBytes, expectedBytes) {
		t.Fatal("recovery changed unrelated site", err)
	}
}

type managedStoppedImageRunner struct {
	image     string
	container string
	failure   error
	commands  []localexec.Command
}

func newManagedStoppedImageRunner(p plan.Plan, d policy.Desired) *managedStoppedImageRunner {
	repository, _, _ := strings.Cut(string(d.Image), "@")
	raw, _ := json.Marshal([]map[string]any{{"Id": strings.Repeat("d", 64), "Digest": *p.Image.ManifestDigest.Value, "RepoDigests": []string{string(d.Image), repository + "@" + *p.Image.ManifestDigest.Value}, "Os": p.Image.Platform.OS, "Architecture": p.Image.Platform.Arch, "ManifestType": "application/vnd.oci.image.manifest.v1+json"}})
	return &managedStoppedImageRunner{image: string(raw)}
}
func (r *managedStoppedImageRunner) Execute(_ context.Context, command localexec.Command) (localexec.Result, error) {
	r.commands = append(r.commands, command)
	if command.Path != "podman" || command.Mutation {
		return localexec.Result{}, fmt.Errorf("unexpected stopped image mutation")
	}
	if r.failure != nil {
		return localexec.Result{}, r.failure
	}
	if len(command.Args) == 3 && command.Args[0] == "container" && command.Args[1] == "exists" {
		if r.container == "" {
			return localexec.Result{}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
		}
		return localexec.Result{}, nil
	}
	if len(command.Args) == 3 && command.Args[0] == "image" && command.Args[1] == "inspect" {
		return localexec.Result{Stdout: r.image}, nil
	}
	if len(command.Args) == 3 && command.Args[0] == "container" && command.Args[1] == "inspect" {
		return localexec.Result{Stdout: r.container}, nil
	}
	return localexec.Result{}, fmt.Errorf("unexpected stopped image request")
}
func TestManagedStoppedFrontendImageRequiresExactLocalPinsAndOwnership(t *testing.T) {
	for _, name := range []string{"absent", "owned-stopped", "missing-index", "wrong-platform", "foreign-container", "unknown-exists"} {
		t.Run(name, func(t *testing.T) {
			r, p, _, _ := managedFactsRecoveryFixture(t)
			_, desired, err := r.store.LoadPlan(context.Background(), p.Hash)
			if err != nil {
				t.Fatal(err)
			}
			cache := newManagedStoppedImageRunner(p, desired)
			switch name {
			case "missing-index":
				cache.image = strings.ReplaceAll(cache.image, string(desired.Image), "registry.example.com:5000/foreign@sha256:"+strings.Repeat("f", 64))
			case "wrong-platform":
				cache.image = strings.ReplaceAll(cache.image, "arm64", "amd64")
			case "unknown-exists":
				cache.failure = &localexec.Error{Kind: localexec.Timeout}
			case "owned-stopped", "foreign-container":
				image := strings.Repeat("d", 64)
				if name == "foreign-container" {
					image = strings.Repeat("f", 64)
				}
				raw, _ := json.Marshal([]map[string]any{{"Name": "systemd-" + p.App, "Image": image, "State": map[string]any{"Running": false, "Status": "exited"}, "Config": map[string]any{"Labels": map[string]string{"PODMAN_SYSTEMD_UNIT": p.App + ".service"}}}})
				cache.container = string(raw)
			}
			session, err := localexec.NewSession(cache, 1000, t.TempDir(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			runtime := managedRuntime{observer: managedEngineObserver{session: session}}
			err = runtime.stoppedFrontendImage(context.Background(), p, desired)
			if (err == nil) != (name == "absent" || name == "owned-stopped") {
				t.Fatal("cached image proof accepted wrong identity", name, err)
			}
			for _, command := range cache.commands {
				if command.Mutation || command.Args[0] == "manifest" || command.Args[0] == "pull" {
					t.Fatal("stopped proof requested registry or mutation", command.Args)
				}
			}
		})
	}
}
