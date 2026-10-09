package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/policy"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type removalDisk struct {
	Snapshot      target.Snapshot `json:"snapshot"`
	Active        bool            `json:"active"`
	Reloaded      bool            `json:"reloaded"`
	RouteReloaded bool            `json:"route_reloaded"`
}
type removalHost struct {
	*deployRig
	path     string
	boundary func(string)
}

func (h *removalHost) read() (removalDisk, error) {
	var disk removalDisk
	raw, err := os.ReadFile(h.path)
	if err != nil {
		return disk, err
	}
	err = json.Unmarshal(raw, &disk)
	return disk, err
}
func (h *removalHost) write(disk removalDisk, step string) error {
	raw, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	if err = os.WriteFile(h.path+".tmp", raw, 0600); err != nil {
		return err
	}
	if err = os.Rename(h.path+".tmp", h.path); err != nil {
		return err
	}
	if h.boundary != nil {
		h.boundary(step)
	}
	return nil
}
func (h *removalHost) Collect(ctx context.Context) (target.Snapshot, error) {
	disk, err := h.read()
	if err != nil {
		return disk.Snapshot, err
	}
	gen, err := h.store.Generation(ctx)
	disk.Snapshot.Generation = target.Known(gen)
	return disk.Snapshot, err
}
func (h *removalHost) VerifyRemove(context.Context, string, string) error { return nil }
func (h *removalHost) Remove(_ context.Context, name, hash string) error {
	disk, err := h.read()
	if err != nil {
		return err
	}
	if disk.Active {
		return errors.New("fixture writer remains active")
	}
	disk.Snapshot.Apps = target.Known([]target.App{})
	return h.write(disk, "remove_unit")
}

type removalUnits struct {
	*fakeUnits
	host *removalHost
}

func (u removalUnits) VerifyCurrent(ctx context.Context, name string, hashes ...string) error {
	disk, err := u.host.read()
	if err != nil {
		return err
	}
	if disk.Snapshot.Apps.Value == nil {
		return errors.New("unreadable live unit")
	}
	hash := ""
	for _, app := range *disk.Snapshot.Apps.Value {
		if app.QuadletUnits.Value == nil {
			return errors.New("unreadable live unit")
		}
		for _, unit := range *app.QuadletUnits.Value {
			if unit.Name == name {
				hash = unit.Hash
			}
		}
	}
	for _, allowed := range hashes {
		if hash == allowed {
			return nil
		}
	}
	return errors.New("foreign live unit")
}
func (u removalUnits) VerifyRemove(ctx context.Context, name, hash string) error {
	return u.host.VerifyRemove(ctx, name, hash)
}
func (u removalUnits) Remove(ctx context.Context, name, hash string) error {
	return u.host.Remove(ctx, name, hash)
}

type removalRoutes struct {
	fakeRoutes
	host *removalHost
}

func (r removalRoutes) Withdraw(_ context.Context, before caddy.State, app string) (caddy.Result, error) {
	disk, err := r.host.read()
	if err != nil {
		return caddy.Result{}, err
	}
	if disk.Snapshot.CaddyConfig.Value == nil {
		return caddy.Result{}, errors.New("missing route")
	}
	files := map[string]string{}
	for _, file := range disk.Snapshot.CaddyConfig.Value.Files {
		files[file.Name] = file.Hash
	}
	if before.Generation != disk.Snapshot.CaddyConfig.Value.Generation || !maps.Equal(before.Files, files) {
		return caddy.Result{}, errors.New("route drift")
	}
	next := caddy.State{Generation: before.Generation + 1, Files: maps.Clone(files), Sites: maps.Clone(before.Sites)}
	delete(next.Files, app+".caddy")
	delete(next.Sites, app+".caddy")
	disk.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: next.Generation, Files: []target.CaddyFile{}})
	disk.Snapshot.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{})
	disk.RouteReloaded = true
	err = r.host.write(disk, "withdraw_route")
	return caddy.Result{Outcome: caddy.Applied, Next: next}, err
}
func (r removalRoutes) SettleWithdrawal(context.Context, caddy.State, caddy.State, string) error {
	disk, err := r.host.read()
	if err != nil {
		return err
	}
	disk.RouteReloaded = true
	return r.host.write(disk, "withdraw_route")
}

type removalStore struct {
	releases
	host *removalHost
}

func (r removalStore) RetireApp(ctx context.Context, id, app, releaseID string) error {
	if err := r.Store.RetireApp(ctx, id, app, releaseID); err != nil {
		return err
	}
	if r.host.boundary != nil {
		r.host.boundary("retire_app")
	}
	return nil
}
func newRemovalHost(t *testing.T, dir string, seed bool) *removalHost {
	t.Helper()
	r := newDeployRigAt(t, dir)
	h := &removalHost{deployRig: r, path: filepath.Join(dir, "remove-fixture.json")}
	if seed {
		planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
		accepted := r.call(t, "apply", dispatch.ApplyArgs{PlanID: planned.PlanID, IdempotencyKey: "initial"}).Data.(jobs.Accepted)
		if err := r.runner.Run(context.Background(), accepted.OperationID); err != nil {
			t.Fatal(err)
		}
		snap, err := r.inventory.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err = h.write(removalDisk{Snapshot: snap, Active: true}, ""); err != nil {
			t.Fatal(err)
		}
	}
	r.service.Inventory = h
	engine := r.runner.Executor.(Executor).Engine
	engine.Units = removalUnits{r.units, h}
	engine.Routes = removalRoutes{fakeRoutes{r.policy.p}, h}
	engine.Releases = removalStore{releases{r.store}, h}
	manager := engine.Systemd.(*systemd.Fake)
	manager.ShowFunc = func(_ context.Context, u systemd.Unit) (systemd.Properties, error) {
		disk, err := h.read()
		if err != nil {
			return systemd.Properties{}, err
		}
		if u.String() == "hello.service" {
			if disk.Active {
				return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
			}
			if len(*disk.Snapshot.Apps.Value) == 0 {
				return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
			}
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	manager.IsActiveFunc = func(context.Context, systemd.Unit) (bool, error) { disk, err := h.read(); return disk.Active, err }
	manager.StopFunc = func(context.Context, systemd.Unit) error {
		disk, err := h.read()
		if err != nil {
			return err
		}
		if !disk.RouteReloaded {
			return errors.New("stopped before route reload")
		}
		disk.Active = false
		disk.Snapshot.UsedPorts = target.Known([]target.Port{})
		disk.Snapshot.PortOwners = target.Known([]target.PortOwner{})
		return h.write(disk, "stop_unit")
	}
	manager.DaemonReloadFunc = func(context.Context) error {
		disk, err := h.read()
		if err != nil {
			return err
		}
		disk.Reloaded = true
		return h.write(disk, "reload_units")
	}
	engine.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		disk, err := h.read()
		if err != nil {
			return podman.ContainerState{}, err
		}
		if disk.Active {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{Status: "exited"}, nil
	}
	reconciler := newReconciler(r.service, engine, manager)
	r.runner.Executor = Executor{Service: r.service, Engine: engine}
	r.runner.Reconciler = runnerReconciler{reconciler}
	r.runner.Recovery = recoveryJob(reconciler)
	r.server.Reconciler = reconciler
	r.server.Config = apps.Service{Store: r.store, Inventory: h, LoadPolicy: r.policy.Load}
	r.server.Apps = apps.Service{Store: r.store, Inventory: h}
	return h
}
func TestConnectedRemoveNoOpAndStalePlan(t *testing.T) {
	h := newRemovalHost(t, t.TempDir(), true)
	plan1 := h.call(t, "lifecycle", dispatch.LifecycleArgs{App: "hello", Action: plan.RemoveApp}).Data.(apps.ConfigPlan)
	disk, err := h.read()
	if err != nil {
		t.Fatal(err)
	}
	if !disk.Active || disk.RouteReloaded {
		t.Fatal("planning applied effects")
	}
	op := h.call(t, "apply", dispatch.ApplyArgs{PlanID: plan1.PlanID, IdempotencyKey: "remove"}).Data.(jobs.Accepted)
	if err = h.runner.Run(context.Background(), op.OperationID); err != nil {
		t.Fatal(err)
	}
	status := h.call(t, "operation", dispatch.OperationArgs{OperationID: op.OperationID}).Data.(jobs.Status)
	if status.Operation.State != ops.Succeeded || status.Operation.Kind != ops.Deploy {
		t.Fatal(status)
	}
	disk, err = h.read()
	if err != nil || disk.Active || !disk.RouteReloaded || !disk.Reloaded || len(*disk.Snapshot.Apps.Value) != 0 || len(disk.Snapshot.CaddyConfig.Value.Files) != 0 {
		t.Fatal(disk, err)
	}
	report := h.call(t, "status", dispatch.AppStatusArgs{}).Data.(apps.Report)
	if len(report.Apps) != 0 {
		t.Fatal(report)
	}
	before := disk
	for _, app := range []string{"hello", "never-installed"} {
		p := h.call(t, "lifecycle", dispatch.LifecycleArgs{App: app, Action: plan.RemoveApp}).Data.(apps.ConfigPlan)
		if p.Kind != plan.NoOp {
			t.Fatal(p)
		}
		accepted := h.call(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: app + "-noop"}).Data.(jobs.Accepted)
		if err = h.runner.Run(context.Background(), accepted.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := h.read()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("no-op mutated runtime")
	}
	if _, err = h.store.CurrentRelease(context.Background(), "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	stale := h.call(t, "apply", dispatch.ApplyArgs{PlanID: plan1.PlanID, IdempotencyKey: "stale-remove"}).Data.(jobs.Accepted)
	if err = h.runner.Run(context.Background(), stale.OperationID); err == nil {
		t.Fatal("stale removal succeeded")
	}
	events, err := h.store.EventsAfter(context.Background(), stale.OperationID, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Kind == "failure" {
			var p ops.FailurePayload
			json.Unmarshal(event.Payload, &p)
			found = found || p.Code == "stale_plan"
		}
	}
	if !found {
		t.Fatal("stale_plan missing", events)
	}
}

func TestRemoveDoesNotReauthorizeCommittedDeployments(t *testing.T) {
	for _, app := range []string{"hello", "never-installed"} {
		t.Run(app, func(t *testing.T) {
			h := newRemovalHost(t, t.TempDir(), true)
			raw, err := os.ReadFile("../policy/testdata/operator.toml")
			if err != nil {
				t.Fatal(err)
			}
			// The installed app uses ghcr.io. Today's operator policy denies that
			// registry (and its old domain), but withdrawing its route is still allowed.
			raw = bytes.ReplaceAll(raw, []byte("*.Example.com"), []byte("revoked.example.net"))
			h.policy.p, err = policy.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			p := h.call(t, "lifecycle", dispatch.LifecycleArgs{App: app, Action: plan.RemoveApp}).Data.(apps.ConfigPlan)
			accepted := h.call(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "remove-revoked"}).Data.(jobs.Accepted)
			if err = h.runner.Run(context.Background(), accepted.OperationID); err != nil {
				t.Fatal(err)
			}
			status := h.call(t, "operation", dispatch.OperationArgs{OperationID: accepted.OperationID}).Data.(jobs.Status)
			if status.Operation.State != ops.Succeeded {
				t.Fatal(status)
			}
			disk, err := h.read()
			if err != nil {
				t.Fatal(err)
			}
			if app == "hello" && (disk.Active || !disk.RouteReloaded) {
				t.Fatal("revoked app still running", disk)
			}
			if app == "never-installed" && (!disk.Active || disk.RouteReloaded) {
				t.Fatal("no-op touched unrelated revoked app", disk)
			}
		})
	}
}
