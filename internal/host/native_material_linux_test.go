//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type nativeMaterialFixture struct {
	service         Service
	plan            plan.Plan
	desired         policy.Desired
	stateRoot, home string
}

func freshNativeMaterialFixture(t *testing.T) nativeMaterialFixture {
	t.Helper()
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
	app.SpacetimeDB.Module.Metadata = &spec.ModuleMetadata{WasmSHA256: spec.WasmSHA256(strings.Repeat("b", 64)), RuntimeModuleHash: spec.RuntimeModuleHash(strings.Repeat("c", 64)), Canonicalizer: spacetimedb.CanonicalizerVersion, SchemaSHA256: strings.Repeat("d", 64)}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "/srv/brine-data", root, 1) + "\n[[allowed_registries]]\nhost=\"docker.io\"\nrepository_prefixes=[\"clockworklabs\"]\n")
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
	operation, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: planned.Hash}, "deploy:"+strings.Repeat("e", 64), "managed-prepare")
	if err != nil {
		t.Fatal(err)
	}
	uidValue, gidValue := int64(os.Geteuid()), int64(os.Getegid())
	if uidValue < 0 || uidValue > math.MaxUint32 || gidValue < 0 || gidValue > math.MaxUint32 {
		t.Fatal("fixture identity invalid")
		return nativeMaterialFixture{}
	}
	uid, gid := uint32(uidValue), uint32(gidValue)
	session, err := localexec.NewSession(nativeMaterialImageExecutor{}, uid, home, time.Second)
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
	return nativeMaterialFixture{service: Service{Store: state, Policy: &fakePolicy{p: pol}, Requester: "deploy:" + strings.Repeat("e", 64), managedPreparation: preparation, managedObserver: &observer}, plan: planned, desired: desired, stateRoot: stateRoot, home: home}
}

type nativeMaterialImageExecutor struct{}

func (nativeMaterialImageExecutor) Execute(ctx context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path != "/usr/bin/podman" && c.Path != "podman" {
		return localexec.Result{}, fmt.Errorf("unexpected image observation")
	}
	if len(c.Args) > 0 && c.Args[0] == "pull" {
		return localexec.Result{}, ctx.Err()
	}
	if len(c.Args) > 1 && c.Args[0] == "image" && c.Args[1] == "inspect" {
		raw, _ := json.Marshal([]map[string]any{{"Id": strings.Repeat("a", 64), "Digest": data.SpacetimeDBARM64Manifest, "RepoDigests": []string{data.SpacetimeDBImage, "docker.io/clockworklabs/spacetime@" + data.SpacetimeDBARM64Manifest}, "Architecture": "arm64", "Os": "linux", "ManifestType": "application/vnd.oci.image.manifest.v1+json"}})
		return localexec.Result{Stdout: string(raw)}, ctx.Err()
	}
	if len(c.Args) > 0 && c.Args[0] == "ps" && !c.Mutation {
		return localexec.Result{}, ctx.Err()
	}
	return localexec.Result{}, fmt.Errorf("unexpected native material command")
}
func freshNativeMaterialPacket(t *testing.T) []byte {
	f := newNativePacketFixture(t)
	f.now = time.Now().UTC()
	f.claims["iat"] = f.now.Unix()
	return nativePacketJSON(t, f.packet(t))
}
func deliverNativeMaterialFixture(t *testing.T, f nativeMaterialFixture, key string, packet []byte) nativematerial.Receipt {
	t.Helper()
	wire, err := dispatch.EncodeNativeMaterial(f.plan.App, f.plan.Hash, key, packet)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wire)
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: "native_material_set", RequestID: "material-test", Args: wire})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(request)
	server := dispatch.NewServer("fixture", nil).WithJobs(nil, f.service.Authorize)
	server.NativeMaterial = nativeMaterialOperations{service: f.service}
	response, err := server.Handle(context.Background(), bytes.NewReader(request))
	if err != nil || !response.OK {
		t.Fatal("public material delivery failed", err, response.Error)
	}
	receipt, ok := response.Data.(nativematerial.Receipt)
	if !ok || !receipt.Valid() {
		t.Fatal("invalid material receipt")
	}
	return receipt
}
func TestNativeMaterialFreshPublicDeliveryCreatesOnlyUnpublishedRuntime(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	ctx := context.Background()
	if _, _, err := f.service.Store.ReadEngineHead(ctx, f.plan.Managed.Database.BindingID); err == nil {
		t.Fatal("fixture preseeded runtime")
	}
	if _, err := os.Stat(filepath.Join(f.stateRoot, "engine-secrets")); !os.IsNotExist(err) {
		t.Fatal("fixture preseeded native materials")
	}
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	receipt := deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	if receipt.Outcome != "configured_unpublished" || receipt.Secrets.Publisher.Version != 1 {
		t.Fatal("fresh delivery failed", receipt.Outcome)
	}
	head, runtime, err := f.service.Store.ReadEngineHead(ctx, receipt.BindingID)
	if err != nil || head.Version != 1 || runtime.State != data.EngineUnpublished || runtime.Database != "" || runtime.Publisher != "" || runtime.Names == nil || len(runtime.Names) != 0 {
		t.Fatal("material claimed initialized native authority", err)
	}
	for _, role := range []string{"publisher", "signing-private", "signing-public", "auth-policy"} {
		path := filepath.Join(f.stateRoot, "engine-secrets", string(receipt.BindingID), role, "v1.secret")
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 || info.Size() == 0 {
			t.Fatal("actual private native role absent", role, err)
		}
	}
	entries, err := os.ReadDir(f.plan.Managed.Database.SourcePath())
	if err != nil || len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatal("delivery started or initialized native data")
	}
	replay := deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	if replay != receipt {
		t.Fatal("delivery replay changed immutable receipt")
	}
	laterPacket := freshNativeMaterialPacket(t)
	defer clear(laterPacket)
	later := deliverNativeMaterialFixture(t, f, strings.Repeat("2", 32), laterPacket)
	if later.Outcome != "stored_unreferenced" || later.Secrets.Publisher.Version != 2 {
		t.Fatal("later delivery not unreferenced", later.Outcome)
	}
	after, active, err := f.service.Store.ReadEngineHead(ctx, receipt.BindingID)
	if err != nil || after != head || active.Secrets != runtime.Secrets {
		t.Fatal("candidate activated itself", err)
	}
	for _, role := range []string{"publisher", "signing-private", "signing-public", "auth-policy"} {
		path := filepath.Join(f.stateRoot, "engine-secrets", string(receipt.BindingID), role, "v"+strconv.Itoa(2)+".secret")
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := f.service.Store.EventsAfter(ctx, receipt.OperationID, 0, 100)
	if err != nil || len(evidence) != 3 {
		t.Fatal("real delivery operation events", err)
	}
	serialized, _ := json.Marshal(evidence)
	var values map[string]json.RawMessage
	_ = json.Unmarshal(packet, &values)
	for _, role := range []string{"publisher", "signing_private", "signing_public"} {
		var planted string
		_ = json.Unmarshal(values[role], &planted)
		if bytes.Contains(serialized, []byte(planted)) {
			t.Fatal("native material entered events")
		}
	}
}

func failNativeMaterialFixture(t *testing.T, f nativeMaterialFixture, key string, packet []byte) {
	t.Helper()
	wire, err := dispatch.EncodeNativeMaterial(f.plan.App, f.plan.Hash, key, packet)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(wire)
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: "native_material_set", RequestID: "material-test", Args: wire})
	if err != nil {
		t.Fatal(err)
	}
	defer clear(request)
	server := dispatch.NewServer("fixture", nil).WithJobs(nil, f.service.Authorize)
	server.NativeMaterial = nativeMaterialOperations{service: f.service}
	response, err := server.Handle(context.Background(), bytes.NewReader(request))
	if err == nil || response.OK {
		t.Fatal("invalid native scope admitted")
	}
}
func TestNativeMaterialFreshRefusesChangedSourceOwnerPolicyAndFence(t *testing.T) {
	for _, failure := range []string{"source", "inode", "owner", "policy", "fence"} {
		t.Run(failure, func(t *testing.T) { checkNativeMaterialFailure(t, failure) })
	}
}
func checkNativeMaterialFailure(t *testing.T, failure string) {
	t.Helper()
	f := freshNativeMaterialFixture(t)
	ctx := context.Background()
	switch failure {
	case "source":
		if err := os.WriteFile(filepath.Join(f.plan.Managed.Database.SourcePath(), "native-state"), []byte("unexpected history"), 0600); err != nil {
			t.Fatal(err)
		}
	case "inode":
		source := f.plan.Managed.Database.SourcePath()
		if err := os.Rename(source, source+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(source, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte(managedEmptyConfig), 0600); err != nil {
			t.Fatal(err)
		}
	case "owner":
		f.service.Requester = "deploy:" + strings.Repeat("f", 64)
	case "policy":
		f.service.Policy = &fakePolicy{err: fmt.Errorf("changed policy")}
	case "fence":
		// Capture the actual original held fence through the reservation's public
		// permit evidence; neither delivery nor this test may manufacture a fence.
		prepared, err := f.service.Store.ReadManagedPreparation(ctx, f.plan.Managed.Database.BindingID)
		if err != nil {
			t.Fatal(err)
		}
		fence, err := f.service.Store.ReadManagedPreparationFence(ctx, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.service.Store.ReleaseDataFence(ctx, fence, prepared.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	failNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	if _, _, err := f.service.Store.ReadEngineHead(ctx, f.plan.Managed.Database.BindingID); err == nil {
		t.Fatal("refusal created runtime")
	}
	if _, err := os.Stat(filepath.Join(f.stateRoot, "engine-secrets")); !os.IsNotExist(err) {
		t.Fatal("refusal wrote material")
	}
}
func TestNativeMaterialUnknownWriteRetainsVersionsAndNeverRetries(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	ctx := context.Background()
	binding := f.plan.Managed.Database.BindingID
	collided := filepath.Join(f.stateRoot, "engine-secrets", string(binding), "signing-private")
	if err := os.MkdirAll(collided, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(collided, "v1.secret")
	if err := os.WriteFile(path, []byte("foreign retained value"), 0600); err != nil {
		t.Fatal(err)
	}
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	r := deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	if r.Outcome != "recovery_required" {
		t.Fatal("collision produced success")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // Task-owned private collision fixture.
	if err != nil || string(raw) != "foreign retained value" {
		t.Fatal("delivery replaced unknown private file")
	}
	if _, _, err = f.service.Store.ReadEngineHead(ctx, binding); err == nil {
		t.Fatal("unknown write committed runtime")
	}
	publisher := filepath.Join(f.stateRoot, "engine-secrets", string(binding), "publisher", "v1.secret")
	original, err := os.ReadFile(publisher) //nolint:gosec // Task-owned private delivery fixture.
	if err != nil {
		t.Fatal(err)
	}
	defer clear(original)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	replay := deliverNativeMaterialFixture(t, f, strings.Repeat("1", 32), packet)
	if replay != r {
		t.Fatal("replay rewrote unknown reservation")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("replay blindly completed a prior unknown write")
	}
	after, err := os.ReadFile(publisher) //nolint:gosec // Task-owned retained delivery fixture.
	defer clear(after)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("retained material changed")
	}
	failNativeMaterialFixture(t, f, strings.Repeat("2", 32), packet)
	status, err := f.service.Store.NativeMaterialReceipt(ctx, r.OperationID, f.service.Requester)
	if err != nil || status != r {
		t.Fatal("metadata-only inspection lost unknown delivery", err)
	}
}

func TestNativeMaterialReservationCannotClaimInitializedAuthority(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	ctx := context.Background()
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	material, expiry, err := decodeNativeMaterialPacket(packet, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer material.Clear()
	candidate, err := (nativeMaterialOperations{service: f.service}).candidate(ctx, f.plan, f.desired, material, expiry)
	if err != nil {
		t.Fatal(err)
	}
	candidate.State = data.EngineInitialized
	candidate.Database = data.NativeDatabaseIdentity(strings.Repeat("a", 64))
	candidate.Publisher = data.NativePublisherIdentity(strings.Repeat("b", 64))
	candidate.Names = []string{"game"}
	if candidate.Validate() != nil {
		t.Fatal("negative fixture invalid")
	}
	if _, _, err = f.service.Store.ReserveNativeMaterial(ctx, f.plan.App, f.plan.Hash, f.service.Requester, strings.Repeat("1", 32), candidate); err == nil {
		t.Fatal("declared material granted initialized database authority")
	}
}

func TestNativeMaterialLostResponseInspectionNeedsNoPacketOrCurrentLease(t *testing.T) {
	f := freshNativeMaterialFixture(t)
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	key := strings.Repeat("1", 32)
	delivered := deliverNativeMaterialFixture(t, f, key, packet)
	// A read-only metadata lookup has no publisher packet and no live policy
	// dependency. Failed future authorization cannot hide retained operation facts.
	f.service.Policy = &fakePolicy{err: fmt.Errorf("current policy unavailable")}
	state, err := store.OpenReadOnly(context.Background(), f.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	lookup := dispatch.NativeMaterialInspectArgs{App: f.plan.App, PreparationPlanID: f.plan.Hash, IdempotencyKey: key}
	observed, err := inspectNativeMaterial(context.Background(), state, f.service.Requester, lookup)
	if err != nil || observed != delivered {
		t.Fatal("lost response could not be inspected without material", err)
	}
	foreign := lookup
	foreign.App = "other-game"
	if _, err = inspectNativeMaterial(context.Background(), state, f.service.Requester, foreign); err == nil {
		t.Fatal("foreign app retrieved key receipt")
	}
	if _, err = inspectNativeMaterial(context.Background(), state, "deploy:"+strings.Repeat("f", 64), lookup); err == nil {
		t.Fatal("foreign requester retrieved key receipt")
	}
}
