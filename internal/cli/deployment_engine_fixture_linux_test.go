package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/host"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

type deploymentEngineFixture struct {
	t        *testing.T
	ctx      context.Context
	cancel   context.CancelFunc
	state    *store.Store
	server   *dispatch.Server
	runner   jobs.Runner
	snapshot target.Snapshot
	pol      policy.Policy
	image    plan.Image
	path     string
	flags    operationFlags

	readers   sync.WaitGroup
	mu        sync.Mutex
	requests  []dispatch.Request
	effects   []string
	launches  []string
	unitHash  string
	routeHash string
	active    bool
	pullFail  bool
	gate      chan struct{}
	pullGate  chan struct{}
	pulling   chan struct{}
	done      chan struct{}
	runErr    error
}

func newDeploymentEngineFixture(t *testing.T, pullFail, holdPull bool) *deploymentEngineFixture {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	f := &deploymentEngineFixture{t: t, ctx: ctx, cancel: cancel, pullFail: pullFail, gate: make(chan struct{}), pullGate: make(chan struct{}), pulling: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(f.cleanup)
	var err error
	f.state, err = store.OpenContext(ctx, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot, err = target.Decode(deploymentEngineBytes(t, "../target/testdata/ready-arm64.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.pol, err = policy.Parse(bytes.ReplaceAll(deploymentEngineBytes(t, "../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	f.image = plan.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64))}
	raw := bytes.ReplaceAll(deploymentEngineBytes(t, "../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello"))
	f.path = deploymentEngineSpec(t, root, raw)
	f.flags = operationFlags{target: "fixture", configDir: filepath.Join(root, "targets")}
	service := host.Service{Store: f.state, Inventory: f, Policy: f, Images: f, Requester: "a3-fixture"}
	runtime := &podman.Fake{PullFunc: f.pull, InspectFunc: f.inspect, ContainerStateFunc: f.containerState}
	manager := &systemd.Fake{DaemonReloadFunc: f.reload, StartFunc: f.start, IsActiveFunc: f.isActive, ShowFunc: f.show, JobPendingFunc: f.jobPending}
	engine := apply.Executor{Journal: f.state, Releases: deploymentEngineReleases{f.state}, Plans: f.state, Podman: runtime, Systemd: manager, Units: f, Routes: f, Health: f, EffectTimeout: 5 * time.Second}
	f.runner = jobs.Runner{Store: f.state, Executor: host.Executor{Service: service, Engine: engine}}
	f.server = dispatch.NewServer("a3-fixture", f).WithJobs(jobs.Service{Store: f.state, Launcher: f, Requester: service.Requester}, service.Authorize)
	f.server.Planner, f.server.Apps = service, apps.Service{Store: f.state, Inventory: f}
	f.server.Diagnose = deploymentEngineDiagnosis{f}
	f.server.Logs = nil
	if !holdPull {
		close(f.pullGate)
	}
	return f
}

func deploymentEngineBytes(t *testing.T, path string) []byte {
	t.Helper()
	root, err := os.OpenRoot("..")
	if err != nil {
		t.Fatal(err)
	}
	defer deploymentEngineCloseRoot(t, root)
	raw, err := root.ReadFile(strings.TrimPrefix(path, "../"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *deploymentEngineFixture) cleanup() {
	f.cancel()
	f.mu.Lock()
	launched := len(f.launches) != 0
	f.mu.Unlock()
	if launched {
		deploymentEngineWait(f.t, f.done, "worker cleanup")
	}
	joined := make(chan struct{})
	go func() { f.readers.Wait(); close(joined) }()
	deploymentEngineWait(f.t, joined, "dispatcher readers cleanup")
	if f.state != nil {
		if err := f.state.Close(); err != nil {
			f.t.Error(err)
		}
	}
}

func deploymentEngineWait(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s did not finish", label)
	}
}

func (f *deploymentEngineFixture) Launch(_ context.Context, id systemd.OperationID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.launches) != 0 {
		return f.unexpected("second runner launch")
	}
	f.launches = append(f.launches, id.String())
	go f.work(id.String())
	return nil
}

func (f *deploymentEngineFixture) work(id string) {
	defer close(f.done)
	select {
	case <-f.ctx.Done():
		return
	case <-f.gate:
	}
	f.runErr = f.runner.Run(f.ctx, id)
}

func (f *deploymentEngineFixture) finish() {
	deploymentEngineWait(f.t, f.done, "detached runner")
	if f.pullFail && f.runErr == nil || !f.pullFail && f.runErr != nil {
		f.t.Fatalf("runner outcome %v, pull failure %v", f.runErr, f.pullFail)
	}
}

func (f *deploymentEngineFixture) record(effect string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.effects = append(f.effects, effect)
}

func (f *deploymentEngineFixture) Load(context.Context) (policy.Policy, error) { return f.pol, nil }

func (f *deploymentEngineFixture) Resolve(_ context.Context, ref spec.ImageReference, platform target.Platform) (plan.Image, error) {
	if string(ref) != "ghcr.io/team/hello@"+f.image.Digest || platform != f.image.Platform {
		return plan.Image{}, f.unexpected("image resolution")
	}
	return f.image, nil
}

func (f *deploymentEngineFixture) Collect(ctx context.Context) (target.Snapshot, error) {
	snap := f.snapshot
	generation, err := f.state.Generation(ctx)
	if err != nil {
		return snap, err
	}
	snap.Generation = target.Known(generation)
	release, err := f.state.CurrentRelease(ctx, "hello")
	if errors.Is(err, store.ErrNotFound) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	snap.Apps = target.Known([]target.App{{Name: "hello", Image: target.Known(target.Image{Digest: release.Image.Digest, Platform: release.Image.Platform}), AllocatedHostPort: target.Known(release.HostPort), QuadletUnits: target.Known(release.Units), Secrets: target.Known([]target.Secret{}), UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}})
	snap.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: release.CaddyGeneration, Files: []target.CaddyFile{release.CaddyFile}})
	snap.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "hello.caddy", App: "hello", Domains: target.Known([]string{"hello.example.com"})}})
	snap.UsedPorts = target.Known([]target.Port{release.HostPort})
	snap.PortOwners = target.Known([]target.PortOwner{{Port: release.HostPort, App: "hello", Process: "rootlessport", Unit: "hello.service"}})
	return snap, nil
}

func (f *deploymentEngineFixture) pull(ctx context.Context, image podman.Image) error {
	if image.String() != "ghcr.io/team/hello@"+f.image.Digest {
		return f.unexpected("pull")
	}
	f.record("pull")
	close(f.pulling)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.pullGate:
	}
	if f.pullFail {
		return &localexec.Error{Kind: localexec.Failed}
	}
	return nil
}

func (f *deploymentEngineFixture) inspect(_ context.Context, image podman.Image) (podman.ImageInfo, error) {
	if image.String() != "ghcr.io/team/hello@"+f.image.Digest {
		return podman.ImageInfo{}, f.unexpected("inspect")
	}
	f.record("verify_image")
	return podman.ImageInfo{IndexDigest: f.image.Digest, ManifestDigest: *f.image.ManifestDigest.Value, Platform: podman.Platform{OS: "linux", Architecture: "arm64"}}, nil
}

func (f *deploymentEngineFixture) VerifyCurrent(_ context.Context, app string, hashes ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if app != "hello.container" || !slices.Contains(hashes, f.unitHash) {
		return f.unexpected("unit ownership")
	}
	return nil
}

func (f *deploymentEngineFixture) Stage(_ context.Context, unit quadlet.Unit) error {
	if unit.Name() != "hello.container" || !bytes.Contains(unit.Bytes(), []byte("Image=ghcr.io/team/hello@"+*f.image.ManifestDigest.Value)) || !bytes.Contains(unit.Bytes(), []byte("PublishPort=127.0.0.1:20000:3000")) {
		return f.unexpected("staged unit")
	}
	f.record("stage_unit")
	return nil
}

func (f *deploymentEngineFixture) Install(_ context.Context, unit quadlet.Unit, previous string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if unit.Name() != "hello.container" || previous != "" || f.unitHash != "" {
		return f.unexpected("unit installation")
	}
	f.unitHash = unit.Hash()
	f.effects = append(f.effects, "install_unit")
	return nil
}

func (f *deploymentEngineFixture) Rollback(context.Context, string, string, string) error {
	return f.unexpected("unit rollback")
}

func (f *deploymentEngineFixture) reload(context.Context) error { f.record("reload_units"); return nil }

func (f *deploymentEngineFixture) start(_ context.Context, unit systemd.Unit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if unit.String() != "hello.service" || f.unitHash == "" || f.active {
		return f.unexpected("start")
	}
	f.active = true
	f.effects = append(f.effects, "start_unit")
	return nil
}

func (f *deploymentEngineFixture) isActive(_ context.Context, unit systemd.Unit) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if unit.String() != "hello.service" {
		return false, f.unexpected("unit read")
	}
	return f.active, nil
}

func (f *deploymentEngineFixture) show(ctx context.Context, unit systemd.Unit) (systemd.Properties, error) {
	active, err := f.isActive(ctx, unit)
	if err != nil {
		return systemd.Properties{}, err
	}
	if active {
		return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
	}
	return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
}

func (f *deploymentEngineFixture) jobPending(_ context.Context, unit systemd.Unit) (bool, error) {
	if unit.String() != "hello.service" {
		return false, f.unexpected("job read")
	}
	return false, nil
}

func (f *deploymentEngineFixture) containerState(_ context.Context, name podman.Name) (podman.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name.String() != "systemd-hello" {
		return podman.ContainerState{}, f.unexpected("container read")
	}
	if f.active {
		return podman.ContainerState{Running: true, Status: "running"}, nil
	}
	return podman.ContainerState{Status: "exited"}, nil
}

func (f *deploymentEngineFixture) Check(_ context.Context, desired policy.Desired, port target.Port, routed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if string(desired.Name) != "hello" || port != 20000 || !f.active {
		return f.unexpected("health probe")
	}
	f.effects = append(f.effects, fmt.Sprintf("health_routed=%t", routed))
	return nil
}

func (f *deploymentEngineFixture) Publish(_ context.Context, before caddy.State, p plan.Plan, desired policy.Desired) (caddy.Result, error) {
	if p.App != "hello" || p.Lifecycle != "" || p.HostPort != 20000 || before.Generation != 0 {
		return caddy.Result{}, f.unexpected("route publication")
	}
	site, err := caddy.NewSite(desired.App(), f.pol, spec.Port(20000))
	if err != nil {
		return caddy.Result{}, err
	}
	raw, err := caddy.Render(site)
	if err != nil {
		return caddy.Result{}, err
	}
	f.record("publish_route")
	f.routeHash = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	next := caddy.State{Generation: 1, Files: map[string]string{"hello.caddy": f.routeHash}, Sites: map[string]caddy.Site{"hello.caddy": site}}
	return caddy.Result{Outcome: caddy.Applied, Current: 1, Next: next}, nil
}

func (f *deploymentEngineFixture) Restore(context.Context, caddy.State, caddy.State) error {
	return f.unexpected("route restore")
}

type deploymentEngineReleases struct{ *store.Store }

func (r deploymentEngineReleases) CurrentRelease(ctx context.Context, app string) (apply.Release, bool, error) {
	release, err := r.Store.CurrentRelease(ctx, app)
	if errors.Is(err, store.ErrNotFound) {
		return release, false, nil
	}
	return release, err == nil, err
}

func (f *deploymentEngineFixture) Call(ctx context.Context, selected transport.Target, request dispatch.Request) (result.Envelope, error) {
	f.readers.Add(1)
	defer f.readers.Done()
	if selected.Name != f.flags.target || !slices.Contains([]string{"plan", "apply", "operation", "status", "diagnose", "deploy_state", "deploy_checkpoint", "deploy_operation"}, request.Op) {
		return result.Envelope{}, f.unexpected("external operation")
	}
	saved := request
	saved.Args = bytes.Clone(request.Args)
	f.mu.Lock()
	f.requests = append(f.requests, saved)
	f.mu.Unlock()
	raw, err := dispatch.EncodeRequest(request)
	if err != nil {
		return result.Envelope{}, err
	}
	response, hostErr := f.server.Handle(ctx, bytes.NewReader(raw))
	wire, err := json.Marshal(response)
	if err != nil {
		return result.Envelope{}, err
	}
	decoded, err := dispatch.DecodeResponse(wire, request.Op)
	return decoded, errors.Join(hostErr, err)
}

func (f *deploymentEngineFixture) dependencies() Dependencies {
	deps := testDependencies(f.t, &bytes.Buffer{}, &bytes.Buffer{})
	deps.Context, deps.OperationClient = f.ctx, f
	deps.LoadOperationTarget = f.loadTarget
	return deps
}

func (f *deploymentEngineFixture) loadTarget(dir, name string) (transport.Target, error) {
	if dir != f.flags.configDir || name != f.flags.target {
		return transport.Target{}, f.unexpected("target lookup")
	}
	return transport.Target{Name: name}, nil
}

func (f *deploymentEngineFixture) machine(args ...string) (result.Envelope, error) {
	var output, diagnostics bytes.Buffer
	deps := f.dependencies()
	deps.Stdout, deps.Stderr = &output, &diagnostics
	args = append(args, "--json", "--no-input", "--target", f.flags.target, "--config-dir", f.flags.configDir)
	err := Execute(deps, args)
	var envelope result.Envelope
	if decodeErr := json.Unmarshal(output.Bytes(), &envelope); decodeErr != nil {
		f.t.Fatalf("command %q JSON: %v: %s %s", args, decodeErr, output.String(), diagnostics.String())
	}
	return envelope, err
}

func deploymentEngineData[T any](t *testing.T, envelope result.Envelope, err error) T {
	t.Helper()
	if err != nil || !envelope.OK {
		t.Fatalf("command refused: %v %+v", err, envelope)
	}
	raw, marshalErr := json.Marshal(envelope.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *deploymentEngineFixture) unexpected(operation string) error {
	f.t.Helper()
	f.t.Errorf("unexpected fixture operation %s", operation)
	return errors.New("unexpected fixture operation")
}

type deploymentEngineDiagnosis struct{ fixture *deploymentEngineFixture }

func (r deploymentEngineDiagnosis) Read(ctx context.Context, request diagnose.Request) (diagnose.Report, error) {
	if request.App != "hello" {
		return diagnose.Report{}, r.fixture.unexpected("diagnosis scope")
	}
	_, err := r.fixture.state.CurrentRelease(ctx, request.App)
	if !errors.Is(err, store.ErrNotFound) {
		return diagnose.Report{}, r.fixture.unexpected("diagnosis of committed app would allow a loopback probe")
	}
	return (diagnose.Reader{Store: r.fixture.state, Inventory: r.fixture, Timeout: 5 * time.Second}).Read(ctx, request)
}

func deploymentEngineSpec(t *testing.T, dir string, content []byte) string {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer deploymentEngineCloseRoot(t, root)
	if err := root.WriteFile("brine.toml", content, 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "brine.toml")
}

func deploymentEngineCloseRoot(t *testing.T, root *os.Root) {
	t.Helper()
	if err := root.Close(); err != nil {
		t.Error(err)
	}
}
