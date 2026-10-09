package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type fakePolicy struct {
	p   policy.Policy
	err error
}

func (p *fakePolicy) Load(context.Context) (policy.Policy, error) { return p.p, p.err }

type fakeImages struct{ image plan.Image }

func (i *fakeImages) Resolve(context.Context, spec.ImageReference, target.Platform) (plan.Image, error) {
	return i.image, nil
}

type fakeLaunch struct{ ids []string }

func (l *fakeLaunch) Launch(_ context.Context, id systemd.OperationID) error {
	l.ids = append(l.ids, id.String())
	return nil
}

type fakeHealth struct{ calls int }

func (h *fakeHealth) Check(context.Context, policy.Desired, target.Port, bool) error {
	h.calls++
	return nil
}

type fakeUnits struct{ installs int }

func (*fakeUnits) Stage(context.Context, quadlet.Unit) error              { return nil }
func (u *fakeUnits) Install(context.Context, quadlet.Unit, string) error  { u.installs++; return nil }
func (*fakeUnits) Rollback(context.Context, string, string, string) error { return nil }

type fakeRoutes struct{ pol policy.Policy }

func (r fakeRoutes) Publish(_ context.Context, before caddy.State, p plan.Plan, d policy.Desired) (caddy.Result, error) {
	site, err := caddy.NewSite(App(d), r.pol, spec.Port(p.HostPort))
	if err != nil {
		return caddy.Result{}, err
	}
	raw, err := caddy.Render(site)
	if err != nil {
		return caddy.Result{}, err
	}
	next := caddy.State{Generation: before.Generation + 1, Files: map[string]string{p.App + ".caddy": fmt.Sprintf("sha256:%x", sha256.Sum256(raw))}, Sites: map[string]caddy.Site{p.App + ".caddy": site}}
	return caddy.Result{Outcome: caddy.Applied, Previous: before.Generation, Current: next.Generation, Next: next}, nil
}
func (fakeRoutes) Restore(context.Context, caddy.State, caddy.State) error { return nil }

type fakeInventory struct {
	snapshot         target.Snapshot
	store            *store.Store
	generationOffset uint64
}

func (i *fakeInventory) Collect(ctx context.Context) (target.Snapshot, error) {
	snap := i.snapshot
	gen, err := i.store.Generation(ctx)
	if err != nil {
		return snap, err
	}
	snap.Generation = target.Known(gen + i.generationOffset)
	release, err := i.store.CurrentRelease(ctx, "hello")
	if err == store.ErrNotFound {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	_, d, err := i.store.LoadPlan(ctx, release.PlanID)
	if err != nil {
		return snap, err
	}
	snap.Apps = target.Known([]target.App{{Name: "hello", Image: target.Known(target.Image{Digest: release.Image.Digest, Platform: release.Image.Platform}), AllocatedHostPort: target.Known(release.HostPort), QuadletUnits: target.Known(release.Units), Secrets: target.Known([]target.Secret{})}})
	snap.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: release.CaddyGeneration, Files: []target.CaddyFile{release.CaddyFile}})
	domains := []string{}
	for _, d := range d.Domains {
		domains = append(domains, string(d))
	}
	snap.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "hello.caddy", App: "hello", Domains: target.Known(domains)}})
	snap.UsedPorts = target.Known([]target.Port{release.HostPort})
	snap.PortOwners = target.Known([]target.PortOwner{{Port: release.HostPort, App: "hello", Process: "rootlessport", Unit: "hello.service"}})
	return snap, nil
}

type deployRig struct {
	store     *store.Store
	service   Service
	inventory *fakeInventory
	policy    *fakePolicy
	images    *fakeImages
	launcher  *fakeLaunch
	runner    jobs.Runner
	server    *dispatch.Server
	pulls     int
	units     *fakeUnits
	health    *fakeHealth
	spec      string
}

func newDeployRig(t *testing.T) *deployRig {
	t.Helper()
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	state, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	snap, err := target.Decode(read("../target/testdata/ready-arm64.json"))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	text := string(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	r := &deployRig{store: state, inventory: &fakeInventory{snapshot: snap, store: state}, policy: &fakePolicy{p: pol}, images: &fakeImages{image: plan.Image{Digest: "sha256:" + strings.Repeat("a", 64), Platform: target.Platform{OS: "linux", Arch: "arm64"}, ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64))}}, launcher: &fakeLaunch{}, units: &fakeUnits{}, health: &fakeHealth{}, spec: text}
	r.service = Service{Store: state, Inventory: r.inventory, Policy: r.policy, Images: r.images, Requester: "fixture-deploy-key"}
	runtime := &podman.Fake{PullFunc: func(context.Context, podman.Image) error { r.pulls++; return nil }, InspectFunc: func(context.Context, podman.Image) (podman.ImageInfo, error) {
		return podman.ImageInfo{IndexDigest: r.images.image.Digest, ManifestDigest: *r.images.image.ManifestDigest.Value, Platform: podman.Platform{OS: "linux", Architecture: "arm64"}}, nil
	}}
	manager := &systemd.Fake{DaemonReloadFunc: func(context.Context) error { return nil }, StartFunc: func(context.Context, systemd.Unit) error { return nil }, IsActiveFunc: func(context.Context, systemd.Unit) (bool, error) { return true, nil }}
	engine := apply.Executor{Journal: state, Releases: releases{state}, Plans: state, Podman: runtime, Systemd: manager, Units: r.units, Routes: fakeRoutes{pol}, Health: r.health}
	r.runner = jobs.Runner{Store: state, Executor: Executor{Service: r.service, Engine: engine}}
	r.server = dispatch.NewServer("fixture", r.inventory).WithJobs(jobs.Service{Store: state, Launcher: r.launcher, Requester: r.service.Requester}, r.service.Authorize)
	r.server.Planner = r.service
	r.server.Apps = apps.Service{Store: state, Inventory: r.inventory}
	return r
}
func (r *deployRig) call(t *testing.T, op string, args any) result.Envelope {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	request, err := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: op, RequestID: "fixture", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := r.server.Handle(context.Background(), bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := dispatch.DecodeResponse(wire, op)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
func TestConnectedDeployAndIdempotency(t *testing.T) {
	r := newDeployRig(t)
	ctx := context.Background()
	planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	if planned.Kind != plan.Create {
		t.Fatalf("plan = %+v", planned)
	}
	accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "fixture-key"}).Data.(jobs.Accepted)
	again := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "fixture-key"}).Data.(jobs.Accepted)
	if again != accepted || len(r.launcher.ids) != 1 {
		t.Fatal("duplicate launch")
	}
	if err := r.runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	status := r.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
	if status.Operation.State != ops.Succeeded || len(status.Events) < 10 || r.pulls != 1 || r.units.installs != 1 || r.health.calls != 2 {
		t.Fatalf("incomplete execution %+v pulls=%d installs=%d health=%d", status, r.pulls, r.units.installs, r.health.calls)
	}
	report := r.call(t, "status", dispatch.AppStatusArgs{App: "hello"}).Data.(apps.Report)
	if len(report.Apps) != 1 || report.Apps[0].Current.PlanID != planned.PlanID || report.Apps[0].Drift.State != "in_sync" {
		t.Fatalf("status %+v", report)
	}
	noOp := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	if noOp.Kind != plan.NoOp {
		t.Fatalf("idempotent plan %+v", noOp)
	}
	acceptedNoOp := r.call(t, "apply", dispatch.ApplyArgs{PlanID: noOp.PlanID, IdempotencyKey: "fixture-noop"}).Data.(jobs.Accepted)
	if err := r.runner.Run(ctx, acceptedNoOp.OperationID); err != nil {
		t.Fatal(err)
	}
	if r.pulls != 1 || r.units.installs != 1 {
		t.Fatal("no-op mutated runtime")
	}
	gen, err := r.store.Generation(ctx)
	if err != nil || gen != 1 {
		t.Fatalf("generation %d %v", gen, err)
	}
}
func TestStalePlanRefusedUnderHostLock(t *testing.T) {
	changes := map[string]func(*deployRig){
		"identity":   func(r *deployRig) { r.inventory.snapshot.Identity.ID = "changed-fixture" },
		"generation": func(r *deployRig) { r.inventory.generationOffset = 1 },
		"versions":   func(r *deployRig) { r.inventory.snapshot.Versions.Systemd = target.Known("257.1") },
		"ports":      func(r *deployRig) { r.inventory.snapshot.UsedPorts = target.Known([]target.Port{20000}) },
		"policy_content": func(r *deployRig) {
			raw, err := os.ReadFile("../policy/testdata/operator.toml")
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io"))
			raw = bytes.ReplaceAll(raw, []byte("memory_mb = 512"), []byte("memory_mb = 256"))
			pol, err := policy.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			r.policy.p = pol
		},
		"routing_generation": func(r *deployRig) {
			r.inventory.snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 1, Files: []target.CaddyFile{}})
		},
		"policy_missing":   func(r *deployRig) { r.policy.err = fmt.Errorf("missing policy") },
		"image_digest":     func(r *deployRig) { r.images.image.Digest = "sha256:" + strings.Repeat("b", 64) },
		"unknown_manifest": func(r *deployRig) { r.images.image.ManifestDigest = target.Observation[string]{Status: target.Unknown} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r := newDeployRig(t)
			p := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
			op := r.call(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "fixture-key"}).Data.(jobs.Accepted)
			change(r)
			err := r.runner.Run(context.Background(), op.OperationID)
			if err == nil || result.ExitCode(err) != 5 {
				t.Fatalf("stale refusal %v category %d", err, result.ExitCode(err))
			}
			status := r.call(t, "operation", dispatch.OperationArgs{OperationID: op.OperationID}).Data.(jobs.Status)
			if status.Operation.State != ops.Failed || r.pulls != 0 || r.units.installs != 0 || r.health.calls != 0 {
				t.Fatalf("stale plan mutated runtime %+v", status)
			}
			found := false
			for _, event := range status.Events {
				if bytes.Contains(event.Payload, []byte("stale_plan")) {
					found = true
				}
			}
			if !found {
				t.Fatal("missing stale-plan event")
			}
		})
	}
}
func TestPlanningDependenciesFailClosed(t *testing.T) {
	for _, name := range []string{"policy", "store", "requester"} {
		t.Run(name, func(t *testing.T) {
			r := newDeployRig(t)
			s := r.service
			switch name {
			case "policy":
				s.Policy = nil
			case "store":
				s.Store = nil
			case "requester":
				s.Requester = ""
			}
			r.server.Planner = s
			raw, _ := json.Marshal(dispatch.PlanArgs{Spec: r.spec})
			request, _ := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "plan", RequestID: "fixture", Args: raw})
			envelope, err := r.server.Handle(context.Background(), bytes.NewReader(request))
			if err == nil || envelope.OK {
				t.Fatal("missing dependency accepted")
			}
			if len(r.launcher.ids) != 0 || r.pulls != 0 {
				t.Fatal("unwired request mutated runtime")
			}
		})
	}
}

func TestApplyAuthorizationFailsClosed(t *testing.T) {
	for _, name := range []string{"policy", "store", "requester"} {
		t.Run(name, func(t *testing.T) {
			r := newDeployRig(t)
			p := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
			service := r.service
			switch name {
			case "policy":
				service.Policy = nil
			case "store":
				service.Store = nil
			case "requester":
				service.Requester = ""
			}
			r.server = r.server.WithJobs(jobs.Service{Store: service.Store, Launcher: r.launcher, Requester: service.Requester}, service.Authorize)
			raw, _ := json.Marshal(dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "fixture-key"})
			request, _ := dispatch.EncodeRequest(dispatch.Request{SchemaVersion: 1, Op: "apply", RequestID: "fixture", Args: raw})
			response, err := r.server.Handle(context.Background(), bytes.NewReader(request))
			if err == nil || response.OK || len(r.launcher.ids) != 0 {
				t.Fatal("unauthorized apply launched")
			}
		})
	}
}

type lockCheckingImages struct {
	delegate ImageResolver
	store    *store.Store
	checked  bool
	t        *testing.T
}

func (i *lockCheckingImages) Resolve(ctx context.Context, ref spec.ImageReference, p target.Platform) (plan.Image, error) {
	wait, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	lock, err := i.store.AcquireHostLock(wait)
	if err == nil {
		lock.Release()
		i.t.Fatal("freshness read ran without host lock")
	}
	i.checked = true
	return i.delegate.Resolve(ctx, ref, p)
}
func TestFreshnessReadsHoldLock(t *testing.T) {
	r := newDeployRig(t)
	p := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	op := r.call(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "fixture-key"}).Data.(jobs.Accepted)
	images := &lockCheckingImages{delegate: r.images, store: r.store, t: t}
	executor := r.runner.Executor.(Executor)
	executor.Service.Images = images
	r.runner.Executor = executor
	if err := r.runner.Run(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	if !images.checked {
		t.Fatal("freshness not checked")
	}
}
