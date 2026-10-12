//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type firstOperatorFixture struct {
	service   Service
	app       spec.App
	raw       string
	factory   *ServerFactory
	runtime   *Runtime
	stateRoot string
}

func newFirstOperatorFixture(t *testing.T) firstOperatorFixture {

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
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "/srv/brine-data", root, 1))
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), string(app.SpacetimeDB.Image), data.SpacetimeDBImage, 1))
	clientRaw := string(raw)
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
	uidValue, gidValue := int64(os.Geteuid()), int64(os.Getegid())
	if uidValue < 0 || uidValue > math.MaxUint32 || gidValue < 0 || gidValue > math.MaxUint32 {
		t.Fatal("fixture identity invalid")
		return firstOperatorFixture{}
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
	service := Service{Store: state, Policy: &fakePolicy{p: pol}, Requester: "deploy:" + strings.Repeat("e", 64), Inventory: &fakeInventory{snapshot: snapshot, store: state}, Images: &fakeImages{image: image}, Data: DataFacts{Store: state, StateRoot: stateRoot, ExcludedRoots: []string{stateRoot}}, managedPreparation: preparation, managedObserver: &observer}
	engine := apply.Executor{Journal: state, Plans: state, ManagedPreparation: preparation}
	launcher := &fakeLaunch{}
	runtime := &Runtime{Planner: service, Authorize: service.Authorize, Jobs: jobs.Service{Store: state, Launcher: launcher, Requester: service.Requester}, NativeMaterial: nativeMaterialOperations{service: service}, Runner: jobs.Runner{Store: state, Executor: Executor{Service: service, Engine: engine}}, close: func() error { return nil }}
	factory := newServerFactory("fixture", "deploy", func(_ context.Context, marker string) (*Runtime, error) {
		if marker != "deploy" {
			t.Fatal("wrong captured authority")
		}
		return runtime, nil
	}, nil)
	t.Cleanup(func() { _ = factory.Close() })
	return firstOperatorFixture{service: service, app: app, raw: clientRaw, factory: factory, runtime: runtime, stateRoot: stateRoot}
}

func firstOperatorCall(t *testing.T, f firstOperatorFixture, operation string, args any) result.Envelope {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: operation, RequestID: "first-operator", Args: raw})
	if err != nil {
		t.Fatal("public request", err)
	}
	defer clear(request)
	server := dispatch.NewServer("fixture", nil)
	server.Factory = f.factory.Build
	response, err := server.Handle(context.Background(), bytes.NewReader(request))
	if err != nil || !response.OK {
		t.Fatal("public factory operation", operation, err, response.Error)
	}
	return response
}
func TestFirstOperatorKnownFactsFactoryPlanApplyMaterial(t *testing.T) {
	f := newFirstOperatorFixture(t)
	ctx := context.Background()
	if _, err := f.service.Store.ReadManagedApp(ctx, string(f.app.Name)); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("fresh setup was preseeded", err)
	}
	direct, err := f.service.Plan(ctx, f.app)
	if err != nil || direct.Kind != plan.Create || len(direct.Conflicts) != 0 {
		t.Fatal("fresh Service.Plan did not admit preparation", err, direct.Kind, direct.Conflicts)
	}
	input, err := dispatch.NewPlanArgs(f.raw, f.app)
	if err != nil {
		t.Fatal(err)
	}
	response := firstOperatorCall(t, f, "plan", input)
	planned, ok := response.Data.(dispatch.Planned)
	if !ok || planned.Kind != plan.Create || planned.Managed == nil || planned.Managed.State != data.EngineUnpublished || len(planned.Conflicts) != 0 {
		t.Fatal("real factory returned unsupported preview", planned.Kind, planned.Conflicts)
	}
	saved, desired, err := f.service.Store.LoadPlan(ctx, planned.PlanID)
	if err != nil || saved.Managed == nil || len(saved.Changes) != 1 || saved.Changes[0].Kind != plan.PrepareManagedEngine || desired.SpacetimeDB.Module.Metadata == nil {
		t.Fatal("public plan was not an executable unpublished allocation", err)
	}
	acceptedResponse := firstOperatorCall(t, f, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: strings.Repeat("1", 32)})
	accepted, ok := acceptedResponse.Data.(jobs.Accepted)
	if !ok || accepted.Status != "accepted" {
		t.Fatal("public apply refused executable plan")
	}
	if err = f.runtime.Runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal("real host executor/public apply failed", err)
	}
	prepared, err := f.service.Store.ReadManagedPreparation(ctx, saved.Managed.Database.BindingID)
	if err != nil || prepared.OperationID != accepted.OperationID {
		t.Fatal("apply did not create its own preparation", err)
	}
	if _, _, err = f.service.Store.ReadEngineHead(ctx, saved.Managed.Database.BindingID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("apply preseeded native runtime", err)
	}
	if _, err = os.Stat(filepath.Join(f.stateRoot, "engine-secrets")); !os.IsNotExist(err) {
		t.Fatal("apply preseeded private native roles")
	}
	packet := freshNativeMaterialPacket(t)
	defer clear(packet)
	materialArgs, err := dispatch.EncodeNativeMaterial(saved.App, saved.Hash, strings.Repeat("2", 32), packet)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(materialArgs)
	materialResponse := firstOperatorCall(t, f, "native_material_set", materialArgs)
	receipt, ok := materialResponse.Data.(nativematerial.Receipt)
	if !ok || receipt.Outcome != "configured_unpublished" {
		t.Fatal("factory material delivery failed")
	}
	_, runtime, err := f.service.Store.ReadEngineHead(ctx, saved.Managed.Database.BindingID)
	if err != nil || runtime.State != data.EngineUnpublished || runtime.Publisher != "" || runtime.Database != "" || len(runtime.Names) != 0 {
		t.Fatal("material claimed initialized authority", err)
	}
	for _, role := range []string{"publisher", "signing-private", "signing-public", "auth-policy"} {
		if _, err := os.Stat(filepath.Join(f.stateRoot, "engine-secrets", string(runtime.BindingID), role, "v1.secret")); err != nil {
			t.Fatal("real private role absent", role, err)
		}
	}
	if _, err = f.service.Store.ReadManagedPreparationFence(ctx, prepared); err != nil {
		t.Fatal("public first flow lost original fence", err)
	}
}
func TestFirstOperatorUnknownPortsRefusesUntilFactsAreKnown(t *testing.T) {
	f := newFirstOperatorFixture(t)
	inventory := f.service.Inventory.(*fakeInventory)
	inventory.snapshot.UsedPorts = target.Observation[[]target.Port]{Status: target.Unknown}
	proposed, err := f.service.Plan(context.Background(), f.app)
	if err != nil {
		t.Fatal(err)
	}
	if proposed.Kind != plan.Conflict {
		t.Fatal("unknown port facts admitted initial allocation")
	}
	unsupported, unknown := false, false
	for _, conflict := range proposed.Conflicts {
		unsupported = unsupported || conflict.Code == plan.ManagedEngineUnsupported
		unknown = unknown || conflict.Code == plan.UnknownFacts && conflict.Field == "ports"
	}
	if !unsupported || !unknown {
		t.Fatal("unknown ports did not preserve exact safe-preview diagnostics", proposed.Conflicts)
	}
}
