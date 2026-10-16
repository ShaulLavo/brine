//go:build linux

package host

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type withdrawalInventory struct {
	snapshot target.Snapshot
	state    *store.Store
}

func (i withdrawalInventory) Collect(ctx context.Context) (target.Snapshot, error) {
	snapshot := i.snapshot
	generation, err := i.state.Generation(ctx)
	snapshot.Generation = target.Known(generation)
	return snapshot, err
}

type nativeWithdrawalRig struct {
	state    *store.Store
	db       *sql.DB
	service  Service
	original plan.Plan
	desired  policy.Desired
	snapshot target.Snapshot
	adapted  []byte
	policy   policy.Policy
}

func newNativeWithdrawalRig(t *testing.T) nativeWithdrawalRig {
	t.Helper()
	directory := t.TempDir()
	state, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	db, err := sql.Open("sqlite", filepath.Join(directory, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	raw, err := os.ReadFile("testdata/native-withdrawal.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), string(raw)); err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatal("generated fixture violates the normal Store schema", foreignKeys, err)
	}
	release, err := state.CurrentRelease(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	original, desired, err := state.LoadPlan(t.Context(), release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.PrepareEngineLocks(t.Context(), directory, original.Managed.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n[[allowed_registries]]\nhost=\"ghcr.io\"\nrepository_prefixes=[\"shaullavo\"]\n")...)
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
	front := release.Managed.Frontend
	snapshot.Apps = target.Known([]target.App{{Name: original.App, Image: target.Known(target.Image{Digest: front.Image.Digest, Platform: front.Image.Platform}), AllocatedHostPort: target.Known(front.Port), QuadletUnits: target.Known([]target.Unit{front.Unit}), Secrets: target.Known([]target.Secret{})}})
	snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 2, Files: []target.CaddyFile{}})
	adapted, err := os.ReadFile("../caddy/testdata/managed-adapted.json")
	if err != nil {
		t.Fatal(err)
	}
	adapted = []byte(strings.NewReplacer("127.0.0.1:20000", "127.0.0.1:"+strconv.FormatUint(uint64(original.Managed.EnginePort), 10), "127.0.0.1:20001", "127.0.0.1:"+strconv.FormatUint(uint64(original.Managed.Frontend.Port), 10)).Replace(string(adapted)))
	service := Service{Store: state, Inventory: withdrawalInventory{snapshot: snapshot, state: state}, Policy: &fakePolicy{p: pol}, Images: managedFactsImages{frontend: front.Image, engine: original.Managed.EngineImage}}
	service.managedAuthority = &managedFactsRecoveryRuntime{}
	service.managedLoggingFacts = func(context.Context) ([]byte, string, string, error) {
		return adapted, managedServiceHash([]byte("root")), managedServiceHash(adapted), nil
	}
	return nativeWithdrawalRig{state: state, db: db, service: service, original: original, desired: desired, snapshot: snapshot, adapted: adapted, policy: pol}
}

func (r nativeWithdrawalRig) facts(t *testing.T) (apply.Facts, error) {
	t.Helper()
	facts, err := r.service.facts(t.Context(), App(r.desired))
	if err == nil {
		facts.Input.Managed.NativeState = target.Known("stopped")
		facts.Input.Managed.OwnedNativeState = target.Known("stopped")
	}
	return facts, err
}

type withdrawalCaddy struct {
	adapted []byte
	reloads int
}

func TestNativePreparationHonorsExistingRouteWithdrawal(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "gen-2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-2", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	adapter := &withdrawalCaddy{adapted: r.adapted}
	manager, err := caddy.NewManager(root, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	routes, err := nativeCommittedRouteStates(t.Context(), r.service, manager)
	if err != nil || len(routes.Files) != 0 {
		t.Fatal("preparation rejects the prior recorded ingress withdrawal", err)
	}
}

func (*withdrawalCaddy) Validate(context.Context, string) error          { return nil }
func (c *withdrawalCaddy) Adapt(context.Context, string) ([]byte, error) { return c.adapted, nil }
func (c *withdrawalCaddy) ReadLoaded(context.Context) ([]byte, error)    { return c.adapted, nil }
func (c *withdrawalCaddy) Reload(context.Context) error                  { c.reloads++; return nil }

type withdrawalRuntime struct {
	managedRuntime
	engineRunning, frontendRunning bool
	ingressChecks                  int
}

func (r *withdrawalRuntime) BeforeEngineStart(ctx context.Context, p plan.Plan) error {
	_, err := r.expected(ctx, p)
	return err
}
func (r *withdrawalRuntime) EngineReady(ctx context.Context, p plan.Plan) error {
	_, err := r.expected(ctx, p)
	if err != nil {
		return err
	}
	if !r.engineRunning {
		return errors.New("engine stopped")
	}
	return nil
}
func (r *withdrawalRuntime) BeforeFrontendStart(ctx context.Context, op string, p plan.Plan, d policy.Desired, hash string) error {
	permit, err := r.state.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil {
		return err
	}
	if !r.engineRunning || permit.Permit.OperationID != op || permit.Permit.PlanID != p.Hash || permit.Permit.UnitHash != hash {
		return errors.New("frontend scope changed")
	}
	return nil
}
func (r *withdrawalRuntime) BeforeIngress(ctx context.Context, op string, p plan.Plan, d policy.Desired) error {
	r.ingressChecks++
	if !r.frontendRunning {
		return errors.New("frontend stopped")
	}
	return r.BeforeFrontendStart(ctx, op, p, d, r.frontendHash(ctx, p.App))
}
func (r *withdrawalRuntime) frontendHash(ctx context.Context, app string) string {
	permit, err := r.state.ReadManagedFrontendPermit(ctx, app)
	if err != nil {
		return ""
	}
	return permit.Permit.UnitHash
}

func TestManagedLifecyclePublishesProtectedNativeWithdrawal(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	before, err := r.state.ReadManagedRouteState(t.Context(), r.original.App)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := r.facts(t)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Routing.Generation != 2 || facts.Input.Managed.RouteRevision != 1 {
		t.Fatal("withdrawal did not preserve protected revision and current generation", facts.Routing.Generation, len(facts.Routing.Files), facts.Input.Managed.RouteRevision)
	}
	planned, err := plan.BuildLifecycle(facts.Input, plan.StartApp)
	if err != nil || planned.Kind == plan.Conflict {
		t.Fatal(err, planned.Conflicts)
	}
	if _, err = r.state.SavePlan(t.Context(), planned, facts.Input.Desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := r.state.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: planned.Hash}, "requester", "reviewed-start-after-abandonment")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.Mkdir(filepath.Join(root, "gen-2"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("gen-2", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	adapter := &withdrawalCaddy{adapted: r.adapted}
	manager, err := caddy.NewManager(root, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	runtime := &withdrawalRuntime{managedRuntime: managedRuntime{state: r.state, routes: manager, loggingFacts: r.service.managedLoggingFacts}}
	routes := apply.GenerationRoutes{Manager: manager, Main: func(context.Context) ([]byte, error) { return []byte("import " + root + "/current/*.caddy\n"), nil }, ManagedSite: func(ctx context.Context, d policy.Desired, m plan.ManagedServicePlan) (caddy.Site, error) {
		site, err := caddy.NewManagedSite(d, r.policy, m)
		if err != nil {
			return caddy.Site{}, err
		}
		return caddy.WithManagedAuthority(site, &managedFactsRecoveryRuntime{})
	}}
	units := &systemd.Fake{StartFunc: func(_ context.Context, unit systemd.Unit) error {
		if unit.String() == planned.App+".service" {
			runtime.frontendRunning = true
		} else {
			runtime.engineRunning = true
		}
		return nil
	}, IsActiveFunc: func(context.Context, systemd.Unit) (bool, error) { return runtime.frontendRunning, nil }, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }, ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if runtime.frontendRunning {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}}
	release, err := r.state.CurrentRelease(t.Context(), planned.App)
	if err != nil {
		t.Fatal(err)
	}
	engine := apply.Executor{Facts: apply.FactsFunc(func(context.Context) (apply.Facts, error) { return facts, nil }), Journal: r.state, Releases: releases{r.state}, Plans: r.state, Managed: runtime, Routes: routes, Systemd: units, Podman: &podman.Fake{ContainerStateFunc: func(context.Context, podman.Name) (podman.ContainerState, error) {
		if runtime.frontendRunning {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{Status: "exited"}, nil
	}}, Units: &fakeUnits{hash: release.Managed.Frontend.Unit.Hash}, Health: &fakeHealth{}}
	if err = engine.Run(t.Context(), op.ID, planned, facts.Input.Desired); err != nil {
		var routeErr *caddy.OperationError
		if errors.As(err, &routeErr) {
			t.Logf("Caddy cause: %v", routeErr.Cause)
		}
		t.Fatalf("%v; cause=%v", err, errors.Unwrap(err))
	}
	completed, err := r.state.GetOperation(t.Context(), op.ID)
	if err != nil || completed.State != ops.Succeeded {
		t.Fatal("lifecycle failed", err)
	}
	after, err := r.state.ReadManagedRouteState(t.Context(), planned.App)
	if err != nil || !after.Present || after.Revision != before.Revision+1 || after.Generation != 3 || after.OperationID != op.ID {
		t.Fatal("normal lifecycle did not record next route", err)
	}
	if adapter.reloads != 1 || runtime.ingressChecks != 1 {
		t.Fatal("route path was not used once", adapter.reloads, runtime.ingressChecks)
	}
	current, err := r.state.CurrentRelease(t.Context(), planned.App)
	if err != nil || current.ID != release.ID {
		t.Fatal("lifecycle replaced original release", err)
	}
}

func TestManagedRoutingRefusesUnprovedNativeWithdrawal(t *testing.T) {
	for _, scenario := range []string{"missing-proof", "extra-observed-site", "changed-generation", "changed-policy", "changed-withdrawal-receipt", "changed-original-requester", "changed-preparation-state", "changed-current-runtime", "changed-incarnation", "changed-previous-manifest"} {
		t.Run(scenario, func(t *testing.T) { testUnprovedNativeWithdrawal(t, scenario) })
	}
}

func testUnprovedNativeWithdrawal(t *testing.T, scenario string) {
	t.Helper()
	r := newNativeWithdrawalRig(t)
	switch scenario {
	case "missing-proof":
		withdrawalSQL(t, r.db, "DROP TRIGGER data_native_prepared_routes_no_delete")
		withdrawalSQL(t, r.db, "DELETE FROM data_native_prepared_routes")
	case "extra-observed-site":
		r.snapshot.CaddyConfig.Value.Files = append(r.snapshot.CaddyConfig.Value.Files, target.CaddyFile{Name: "foreign.caddy", Hash: "sha256:" + managedServiceHash([]byte("valid foreign site bytes"))})
	case "changed-generation":
		r.snapshot.CaddyConfig.Value.Generation++
	case "changed-policy":
		raw, err := os.ReadFile("../policy/testdata/managed-operator.toml")
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.ReplaceAll(string(raw), "managed-preview-1", "managed-preview-2"))
		raw = append(raw, []byte("\n[[allowed_registries]]\nhost=\"ghcr.io\"\nrepository_prefixes=[\"shaullavo\"]\n")...)
		pol, err := policy.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		r.service.Policy = &fakePolicy{p: pol}
	case "changed-withdrawal-receipt":
		mutateWithdrawalReceipt(t, r, func(route *store.NativePreparedRoutes) { route.RecordedAt = route.RecordedAt.Add(-1) })
	case "changed-previous-manifest":
		mutateWithdrawalReceipt(t, r, func(route *store.NativePreparedRoutes) {
			route.PreviousFiles["foreign.caddy"] = "sha256:" + managedServiceHash([]byte("valid extra former site"))
			route.CurrentFiles["foreign.caddy"] = route.PreviousFiles["foreign.caddy"]
		})
	case "changed-original-requester":
		withdrawalSQL(t, r.db, "DROP TRIGGER operation_identity")
		withdrawalSQL(t, r.db, "UPDATE operations SET requester=? WHERE kind='native_prepare'", "deploy:"+strings.Repeat("8", 64))
	case "changed-preparation-state":
		withdrawalSQL(t, r.db, "DROP TRIGGER legal_transition")
		withdrawalSQL(t, r.db, "UPDATE operations SET state='failed' WHERE kind='native_prepare'")
	case "changed-incarnation":
		withdrawalSQL(t, r.db, "DROP TRIGGER data_active_no_update")
		withdrawalSQL(t, r.db, "INSERT INTO data_incarnations VALUES(?,?)", strings.Repeat("d", 32), r.original.App)
		withdrawalSQL(t, r.db, "UPDATE data_active_incarnations SET incarnation_id=? WHERE app=?", strings.Repeat("d", 32), r.original.App)
	case "changed-current-runtime":
		mutateWithdrawalRuntime(t, r)
	default:
		t.Fatal("unknown fixture variant")
	}
	r.service.Inventory = withdrawalInventory{snapshot: r.snapshot, state: r.state}
	if _, err := r.facts(t); err == nil {
		t.Fatal("unproved withdrawal entered production facts", scenario)
	}
}

func withdrawalSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func mutateWithdrawalReceipt(t *testing.T, r nativeWithdrawalRig, mutate func(*store.NativePreparedRoutes)) {
	t.Helper()
	var raw []byte
	if err := r.db.QueryRowContext(t.Context(), "SELECT canonical FROM data_native_prepared_routes WHERE stage='withdrawn'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var route store.NativePreparedRoutes
	if err := json.Unmarshal(raw, &route); err != nil {
		t.Fatal(err)
	}
	mutate(&route)
	canonical, err := json.Marshal(route)
	if err != nil {
		t.Fatal(err)
	}
	withdrawalSQL(t, r.db, "DROP TRIGGER data_native_prepared_routes_no_update")
	withdrawalSQL(t, r.db, "UPDATE data_native_prepared_routes SET canonical=? WHERE stage='withdrawn'", canonical)
}

func mutateWithdrawalRuntime(t *testing.T, r nativeWithdrawalRig) {
	t.Helper()
	var raw []byte
	if err := r.db.QueryRowContext(t.Context(), "SELECT canonical FROM data_engine_runtime_commits").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var runtime data.EngineRuntime
	if err := json.Unmarshal(raw, &runtime); err != nil {
		t.Fatal(err)
	}
	runtime.MemoryMB++
	if err := runtime.Validate(); err != nil {
		t.Fatal("mutated runtime is not a valid guard input", err)
	}
	canonical, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	withdrawalSQL(t, r.db, "DROP TRIGGER data_engine_runtime_no_update")
	withdrawalSQL(t, r.db, "UPDATE data_engine_runtime_commits SET canonical=?", canonical)
}

func TestNativeWithdrawalReaderRejectsForeignRouteScope(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	original, err := r.state.ReadManagedRouteState(t.Context(), r.original.App)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"binding", "epoch", "runtime", "plan", "owner", "file"} {
		head := original
		switch field {
		case "binding":
			head.BindingID = data.ReplicaBindingID(strings.Repeat("d", 32))
		case "epoch":
			head.EpochID = data.ReplicaEpochID(strings.Repeat("9", 32))
		case "runtime":
			head.RuntimeSHA256 = managedServiceHash([]byte("other valid runtime"))
		case "plan":
			head.PlanID = "sha256:" + managedServiceHash([]byte("other valid plan"))
		case "owner":
			head.OperationID = "other-owner"
		case "file":
			head.FileHash = "sha256:" + managedServiceHash([]byte("other valid file"))
		}
		if !head.Valid() {
			t.Fatal("invalid negative input", field)
		}
		if _, err := r.state.NativeRouteWithdrawal(t.Context(), head, r.policy.Hash(), 2); err == nil {
			t.Fatal("foreign route scope granted withdrawal", field)
		}
	}
}
