package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedRollbackFacts struct{ r *rig }

func (f managedRollbackFacts) Collect(context.Context) (target.Snapshot, error) {
	raw, err := target.Encode(f.r.facts.Input.Snapshot)
	if err != nil {
		return target.Snapshot{}, err
	}
	return target.Decode(raw)
}
func (f managedRollbackFacts) ManagedFacts(_ context.Context, in plan.Input) (plan.Input, error) {
	in.Image = f.r.facts.Input.Image
	in.Managed = f.r.facts.Input.Managed
	return in, nil
}

type managedRollbackStore struct {
	*store.Store
	r       *rig
	history []ops.Release
}

func (s *managedRollbackStore) LoadBrineState(context.Context, target.Identity, uint64) (plan.BrineState, error) {
	return s.r.facts.Input.State, nil
}
func (s *managedRollbackStore) PreviousRelease(context.Context, string) (ops.Release, error) {
	return s.history[len(s.history)-2], nil
}
func (s *managedRollbackStore) ReleaseByID(_ context.Context, app, id string) (ops.Release, error) {
	for _, r := range s.history {
		if app == s.r.plan.App && r.ID == id {
			return r, nil
		}
	}
	return ops.Release{}, store.ErrNotFound
}

func managedRollbackHistory(t *testing.T) (*rig, *managedRuntimeFake, apps.Service, *store.Store) {
	t.Helper()
	r := managedInitializedRig(t, true)
	raw, err := os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.ReplaceAll(string(raw), "frontend-token", "token") + "\n[[allowed_registries]]\nhost = \"ghcr.io\"\nrepository_prefixes = [\"shaullavo\"]\n")
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	app := r.desired.App()
	app.Secrets = map[string]spec.SecretReference{"TOKEN": "token"}
	r.desired, err = policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	r.facts.Input.Desired = r.desired
	db, err := store.OpenContext(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	history := &managedRollbackStore{Store: db, r: r}
	runtime := &managedRuntimeFake{rig: r}
	r.executor.Managed = runtime
	for _, version := range []target.Secret{
		{Name: "brine.hello.token.v1", ID: "fixture-token-001"},
		{Name: "brine.hello.token.v2", ID: "fixture-token-002"},
	} {
		commitManagedRollbackVersion(t, r, history, version)
	}
	facts := managedRollbackFacts{r}
	service := apps.Service{Store: history, Inventory: facts, ManagedFacts: facts, LoadPolicy: func(context.Context) (policy.Policy, error) { return pol, nil }}
	return r, runtime, service, db
}

func commitManagedRollbackVersion(t *testing.T, r *rig, db *managedRollbackStore, version target.Secret) {
	t.Helper()
	in := &r.facts.Input
	observed := target.App{Name: "hello", Image: target.Observation[target.Image]{Status: target.Absent}, AllocatedHostPort: target.Observation[target.Port]{Status: target.Absent}, QuadletUnits: target.Known([]target.Unit{}), Secrets: target.Known([]target.Secret{version}), UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}
	if in.Snapshot.Apps.Value != nil && len(*in.Snapshot.Apps.Value) > 0 {
		observed = (*in.Snapshot.Apps.Value)[0]
		observed.Secrets = target.Known(append(slices.Clone(*observed.Secrets.Value), version))
	}
	in.Snapshot.Apps = target.Known([]target.App{observed})
	p, err := plan.Build(*in)
	if err != nil || p.Kind == plan.Conflict {
		t.Fatal(p.Conflicts, err)
	}
	id, err := db.SavePlan(t.Context(), p, r.desired)
	if err != nil {
		t.Fatal(err)
	}
	r.plan = p
	r.state, r.committed, r.events, r.effects = Queued, false, nil, nil
	if err = r.executor.Run(t.Context(), id, p, r.desired); err != nil {
		t.Fatal(err)
	}
	db.history = append(db.history, r.release)
	r.oldPlan, r.oldDesired = p, r.desired
	recollectManagedRollbackRelease(r, p, r.desired, observed.Secrets, uint64(len(db.history)))
	r.state, r.committed, r.events, r.effects, r.intent = Queued, false, nil, nil, ""
}

func recollectManagedRollbackRelease(r *rig, p plan.Plan, desired policy.Desired, observed target.Observation[[]target.Secret], revision uint64) {
	in := &r.facts.Input
	in.Desired, in.Image = desired, p.Image
	m := p.Managed.Clone()
	current := plan.CurrentRelease{App: p.App, ID: r.release.ID, Desired: desired, Image: r.release.Image, HostPort: r.release.HostPort, Secrets: r.release.Secrets, Units: r.release.Units, CaddyFile: r.release.CaddyFile, CaddyGeneration: r.release.CaddyGeneration, Managed: &m}
	in.State.Releases = []plan.CurrentRelease{current}
	front := r.release.Managed.Frontend
	in.Snapshot.Apps = target.Known([]target.App{{Name: p.App, Image: target.Known(target.Image{Digest: p.Image.Digest, Platform: p.Image.Platform}), AllocatedHostPort: target.Known(front.Port), QuadletUnits: target.Known([]target.Unit{front.Unit}), Secrets: observed, UDPPublication: target.Observation[target.UDPPublication]{Status: target.Absent}}})
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.release.CaddyGeneration, Files: []target.CaddyFile{r.release.CaddyFile}})
	r.facts.Routing = caddy.State{Generation: r.release.CaddyGeneration, Files: map[string]string{r.release.CaddyFile.Name: r.release.CaddyFile.Hash}}
	in.Managed.Intent = &plan.ManagedIntentFacts{Revision: revision, EpochID: m.Archive.EpochID, RuntimeSHA256: m.RuntimeSHA256, Current: true, EngineRunning: true, FrontendRunning: true, IngressServing: true}
}

func TestManagedRollbackRetainsHistoricalSecretWithCompleteInventory(t *testing.T) {
	r, runtime, service, db := managedRollbackHistory(t)
	runtime.calls = nil
	before := r.facts.Input.Managed.Clone()
	inventory := slices.Clone(*(*r.facts.Input.Snapshot.Apps.Value)[0].Secrets.Value)
	control, err := plan.Build(r.facts.Input)
	if err != nil || control.Kind != plan.NoOp || control.Secrets[0].VersionName != "brine.hello.token.v2" {
		t.Fatal("unfiltered control did not retain the current version", control.Kind, control.Secrets, err)
	}
	retained, err := service.Store.PreviousRelease(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := service.Rollback(t.Context(), "hello", retained.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, d, err := db.LoadPlan(t.Context(), rollback.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Secrets) != 1 || p.Secrets[0].VersionName != "brine.hello.token.v1" || p.Secrets[0].ID != "fixture-token-001" {
		t.Fatal("historical binding lost", p.Secrets)
	}
	t.Logf("rollback kind %s, historical binding %+v, inventory versions %d", p.Kind, p.Secrets, len(inventory))
	input, _, err := plan.RebuildInput(r.facts.Input, p, []string{"rollback"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := plan.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	drift, driftErr := plan.CompareDecisionInputs(p.DecisionInput(), fresh.DecisionInput())
	t.Logf("fresh rebuild hash matches %t, drift %+v, diagnostic error %v", p.Hash == fresh.Hash, drift, driftErr)
	if p.Kind != plan.Update || !p.SecretExact || p.Hash != fresh.Hash || driftErr != nil || drift.Changed != 0 {
		t.Fatal("host admission rebuild differs", p.Kind, p.SecretExact, p.Hash, fresh.Hash, driftErr)
	}
	var decision struct {
		Snapshot struct {
			Snapshot struct {
				Apps target.Observation[[]target.App] `json:"apps"`
			} `json:"snapshot"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(p.DecisionInput(), &decision); err != nil {
		t.Fatal(err)
	}
	observed := (*decision.Snapshot.Snapshot.Apps.Value)[0].Secrets
	if !reflect.DeepEqual(*observed.Value, inventory) {
		t.Fatal("decision hid retained inventory", observed)
	}
	unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit.Bytes()), "Secret=brine.hello.token.v1,type=env,target=TOKEN") || strings.Contains(string(unit.Bytes()), "token.v2") {
		t.Fatal("frontend unit changed historical binding")
	}
	r.plan = p
	if err = r.executor.Run(t.Context(), "rollback", p, d); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !r.committed || !slices.Equal(r.release.Secrets, p.Secrets) || !slices.Equal(r.release.Managed.Frontend.Secrets, p.Secrets) || r.release.Managed.Frontend.Unit.Hash != unit.Hash() {
		t.Fatal("historical frontend was not committed", r.state)
	}
	for _, call := range runtime.calls {
		if strings.HasPrefix(call, "engine_") && call != "engine_verify" && call != "engine_ready" {
			t.Fatal("rollback changed engine", call)
		}
	}
	if r.release.Managed.EpochID != before.Archive.EpochID || r.release.Managed.BindingID != before.Database.BindingID || r.release.Managed.EngineUnit.Hash != "sha256:"+before.Runtime.UnitSHA256 || len(p.DataAllocations) != 0 || len(p.DataMounts) != 0 || len(p.DataSchemas) != 0 {
		t.Fatal("rollback changed engine/data/epoch")
	}

	after, err := r.facts.Input.Managed.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	original, err := before.CanonicalBytes()
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("engine/data/epoch facts changed", err)
	}
	history := service.Store.(*managedRollbackStore)
	history.history = append(history.history, r.release)
	recollectManagedRollbackRelease(r, p, d, target.Known(inventory), uint64(len(history.history)))
	for _, action := range []plan.ChangeKind{plan.StartApp, plan.StopApp, plan.RestartApp} {
		testManagedLifecycleAfterRollback(t, r, service, db, action, p.Secrets)
	}

}

func testManagedLifecycleAfterRollback(t *testing.T, r *rig, service apps.Service, db *store.Store, action plan.ChangeKind, expected []plan.SecretBinding) {
	t.Helper()
	planned, err := service.Lifecycle(t.Context(), "hello", action)
	if err != nil || planned.Kind == plan.Conflict {
		t.Fatal("lifecycle after rollback refused", action, planned.Conflicts, err)
	}
	p, desired, err := db.LoadPlan(t.Context(), planned.PlanID)
	if err != nil || !p.SecretExact || !slices.Equal(p.Secrets, expected) || !slices.Equal(p.Managed.Frontend.Secrets, expected) {
		t.Fatal("persisted lifecycle lost original binding", action, err)
	}
	r.plan, r.state, r.committed, r.events, r.effects, r.intent = p, Queued, false, nil, nil, ""
	if err := r.executor.Run(t.Context(), "after-rollback-"+string(action), p, desired); err != nil {
		t.Fatal("lifecycle preflight or execution refused exact binding", action, err)
	}
	if r.state != Succeeded || r.committed {
		t.Fatal("lifecycle changed frontend release history", action, r.state, r.committed)
	}
}

func TestManagedRollbackMissingOrForeignRetainedVersionsRefuse(t *testing.T) {
	cases := map[string][]target.Secret{
		"missing":    {{Name: "brine.hello.token.v2", ID: "fixture-token-002"}},
		"changed_id": {{Name: "brine.hello.token.v1", ID: "fixture-token-other"}, {Name: "brine.hello.token.v2", ID: "fixture-token-002"}},
		"foreign":    {{Name: "brine.other.token.v1", ID: "fixture-token-001"}, {Name: "brine.hello.token.v2", ID: "fixture-token-002"}},
	}
	for name, observed := range cases {
		t.Run(name, func(t *testing.T) { testManagedRollbackPlanningRefusal(t, observed) })
	}
}

func testManagedRollbackPlanningRefusal(t *testing.T, observed []target.Secret) {
	t.Helper()
	r, runtime, service, db := managedRollbackHistory(t)
	head := r.release
	runtime.calls = nil
	(*r.facts.Input.Snapshot.Apps.Value)[0].Secrets = target.Known(observed)
	planned, err := service.Rollback(t.Context(), "hello", "")
	if err != nil || planned.Kind != plan.Conflict || !slices.Equal(planned.Conflicts, []plan.Diagnostic{{Code: plan.SecretMissing, Field: "secrets.TOKEN"}}) {
		t.Fatal("missing historical binding did not conflict", planned, err)
	}
	p, d, err := db.LoadPlan(t.Context(), planned.PlanID)
	if err != nil || len(p.Secrets) != 0 || len(p.Changes) != 0 {
		t.Fatal("conflict substituted a binding or kept changes", p.Secrets, p.Changes, err)
	}
	if err := r.executor.Run(t.Context(), "refused", p, d); err == nil {
		t.Fatal("conflict executed")
	}
	if len(runtime.calls) != 0 || len(r.effects) != 0 || r.committed || !reflect.DeepEqual(head, r.release) {
		t.Fatal("missing historical binding caused effects", runtime.calls, r.effects)
	}
}

func TestManagedRollbackFreshDriftRefusesBeforeComponentEffects(t *testing.T) {
	for _, change := range []string{"missing", "changed_id", "new_version", "unknown", "engine_logging"} {
		t.Run(change, func(t *testing.T) { testManagedRollbackFreshDrift(t, change) })
	}
}

func testManagedRollbackFreshDrift(t *testing.T, change string) {
	t.Helper()
	r, runtime, service, db := managedRollbackHistory(t)
	rollback, err := service.Rollback(t.Context(), "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	p, d, err := db.LoadPlan(t.Context(), rollback.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	secrets := &(*r.facts.Input.Snapshot.Apps.Value)[0].Secrets
	switch change {
	case "missing":
		*secrets = target.Known((*secrets.Value)[1:])
	case "changed_id":
		(*secrets.Value)[0].ID = "private-changed-secret-id"
	case "new_version":
		*secrets = target.Known(append(*secrets.Value, target.Secret{Name: "brine.hello.token.v3", ID: "private-new-secret-id"}))
	case "unknown":
		*secrets = target.Observation[[]target.Secret]{Status: target.Unknown}
	case "engine_logging":
		r.facts.Input.Managed.LoggingRootSHA256 = strings.Repeat("e", 64)
	}
	before, err := r.facts.Input.Managed.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	head := r.release
	runtime.calls = nil
	r.plan = p
	if err = r.executor.Run(t.Context(), "rollback", p, d); err == nil {
		t.Fatal("stale rollback applied")
	}
	if len(runtime.calls) != 0 || r.committed || !reflect.DeepEqual(head, r.release) || !slices.Equal(r.effects, []string{"preflight"}) {
		t.Fatal("stale rollback caused component effects", runtime.calls, r.effects)
	}
	after, err := r.facts.Input.Managed.CanonicalBytes()
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refusal changed engine/data/epoch", err)
	}
	assertManagedRollbackDriftEvent(t, r.events)
}

func assertManagedRollbackDriftEvent(t *testing.T, events []Event) {
	t.Helper()
	var found bool
	for _, event := range events {
		if event.Kind != "plan_drift" {
			continue
		}
		var drift plan.DecisionDrift
		if err := json.Unmarshal(event.Payload, &drift); err != nil || !drift.Valid() || drift.Changed == 0 || len(drift.Paths) == 0 {
			t.Fatal("managed drift event invalid", drift, err)
		}
		raw := string(event.Payload)
		if strings.Contains(raw, "private-") || strings.Contains(raw, "fixture-token") || strings.Contains(raw, "brine.hello.token") || strings.Contains(raw, strings.Repeat("e", 64)) {
			t.Fatal("managed drift event exposed values")
		}
		found = true
	}
	if !found {
		t.Fatal("managed drift event missing")
	}
}
