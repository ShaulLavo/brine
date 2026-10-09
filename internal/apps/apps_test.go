package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type fakeStore struct {
	state    plan.BrineState
	previous ops.Release
	releases map[string]ops.Release
	desired  map[string]policy.Desired
	plans    map[string]plan.Plan
	last     ops.Operation
	saved    []plan.Plan
}

func (s *fakeStore) LoadBrineState(_ context.Context, id target.Identity, gen uint64) (plan.BrineState, error) {
	s.state.Target = id
	s.state.Generation = gen
	return s.state, nil
}
func (s *fakeStore) PreviousRelease(context.Context, string) (ops.Release, error) {
	if s.previous.ID == "" {
		return ops.Release{}, store.ErrNotFound
	}
	return s.previous, nil
}
func (s *fakeStore) ReleaseByID(_ context.Context, _ string, id string) (ops.Release, error) {
	if r, ok := s.releases[id]; ok {
		return r, nil
	}
	return ops.Release{}, store.ErrNotFound
}
func (s *fakeStore) LastOperation(context.Context, string) (ops.Operation, error) {
	if s.last.ID == "" {
		return ops.Operation{Kind: ops.Deploy}, store.ErrNotFound
	}
	return s.last, nil
}
func (s *fakeStore) LoadPlan(_ context.Context, id string) (plan.Plan, policy.Desired, error) {
	d, ok := s.desired[id]
	if !ok {
		return plan.Plan{}, d, store.ErrNotFound
	}
	return s.plans[id], d, nil
}
func (s *fakeStore) SavePlan(_ context.Context, p plan.Plan, d policy.Desired) (string, error) {
	s.saved = append(s.saved, p)
	s.desired[p.Hash] = d
	return p.Hash, nil
}

type inventory struct{ snapshot target.Snapshot }

func (i inventory) Collect(context.Context) (target.Snapshot, error) { return i.snapshot, nil }
func fixture(t *testing.T) (Service, *fakeStore, target.Snapshot) {
	t.Helper()
	read := func(path string) []byte {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	snap, e := target.Decode(read("../target/testdata/one-app.json"))
	if e != nil {
		t.Fatal(e)
	}
	pol, e := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if e != nil {
		t.Fatal(e)
	}
	app, e := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if e != nil {
		t.Fatal(e)
	}
	d, e := policy.Normalize(app, pol)
	if e != nil {
		t.Fatal(e)
	}
	a := (*snap.Apps.Value)[0]
	image := plan.Image{Digest: a.Image.Value.Digest, Platform: a.Image.Value.Platform, ManifestDigest: target.Observation[string]{Status: target.Unknown}}
	current := plan.CurrentRelease{App: "hello", ID: "current", Desired: d, Image: image, HostPort: *a.AllocatedHostPort.Value, Secrets: []plan.SecretBinding{}, Units: append([]target.Unit{}, (*a.QuadletUnits.Value)...), CaddyFile: snap.CaddyConfig.Value.Files[0]}
	old := d
	old.Image = spec.ImageReference(strings.ReplaceAll(string(d.Image), strings.Repeat("a", 64), strings.Repeat("e", 64)))
	oldImage := image
	oldImage.Digest = "sha256:" + strings.Repeat("e", 64)
	previous := ops.Release{ID: "previous", PlanID: "old-plan", Image: oldImage}
	s := &fakeStore{state: plan.BrineState{Releases: []plan.CurrentRelease{current}}, previous: previous, releases: map[string]ops.Release{"previous": previous, "current": {ID: "current", PlanID: "current-plan"}}, desired: map[string]policy.Desired{"old-plan": old}, plans: map[string]plan.Plan{"old-plan": {App: "hello", Target: snap.Identity, Image: oldImage}}}
	older := previous
	older.ID = "older"
	older.PlanID = "older-plan"
	s.releases["older"] = older
	s.desired["older-plan"] = old
	s.plans["older-plan"] = s.plans["old-plan"]
	return Service{Store: s, Inventory: inventory{snap}, Probe: probeFunc(func(context.Context, target.Port, policy.Health) (bool, error) { return true, nil })}, s, snap
}
func TestStatus(t *testing.T) {
	for _, kind := range []string{"healthy", "drifted", "failed", "empty", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			service, s, snap := fixture(t)
			active := target.Known(true)
			(*snap.Apps.Value)[0].UnitActive = &active
			if kind == "drifted" {
				(*(*snap.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "sha256:" + strings.Repeat("f", 64)
			}
			if kind == "failed" {
				s.last = ops.Operation{Kind: ops.Deploy, ID: "failed-op", State: ops.Failed}
			}
			if kind == "empty" {
				s.state.Releases = []plan.CurrentRelease{}
			}
			if kind == "unknown" {
				snap.Apps = target.Observation[[]target.App]{Status: target.Unknown}
			}
			service.Inventory = inventory{snap}
			got, e := service.Status(context.Background(), "")
			if e != nil {
				t.Fatal(e)
			}
			if len(s.saved) != 0 {
				t.Fatal("status saved a plan")
			}
			if kind == "empty" {
				if len(got.Apps) != 0 || got.Apps == nil {
					t.Fatal(got)
				}
				return
			}
			a := got.Apps[0]
			if a.Current.ID != "current" || a.Previous.ID != "previous" {
				t.Fatal(a)
			}
			if kind == "healthy" && (a.Health.Direct != "healthy" || a.Drift.State != "in_sync" || a.Health.UnitActive.Value == nil || !*a.Health.UnitActive.Value) {
				t.Fatal(a)
			}
			if kind == "drifted" && a.Drift.State != "drifted" {
				t.Fatal(a)
			}
			if kind == "failed" && a.LastOperation.State != ops.Failed {
				t.Fatal(a)
			}
			if kind == "unknown" && a.Drift.State != "unknown" {
				t.Fatal(a)
			}
		})
	}
}
func TestRollback(t *testing.T) {
	for _, id := range []string{"", "older"} {
		t.Run("target_"+id, func(t *testing.T) {
			service, s, _ := fixture(t)
			got, e := service.Rollback(context.Background(), "hello", id)
			if e != nil {
				t.Fatal(e)
			}
			if got.PlanID == "" || got.ReleaseID != expectedRelease(id) || got.Compatibility != "stateless_compatible" || len(s.saved) != 1 || s.saved[0].Kind != plan.Update {
				t.Fatal(got, s.saved)
			}
			diff := got.Diff.Image
			if diff == nil || diff.From.Digest == diff.To.Digest {
				t.Fatal(got.Diff)
			}
		})
	}
	for _, kind := range []string{"unknown", "current", "compatibility", "app", "no_previous"} {
		t.Run("refuse_"+kind, func(t *testing.T) {
			service, s, _ := fixture(t)
			id := ""
			app := "hello"
			switch kind {
			case "unknown":
				id = "missing"
			case "current":
				id = "current"
			case "compatibility":
				d := s.desired["old-plan"]
				d.SchemaVersion = 2
				s.desired["old-plan"] = d
			case "app":
				app = "other"
			case "no_previous":
				s.previous = ops.Release{}
			}
			_, e := service.Rollback(context.Background(), app, id)
			var domain *result.Error
			if !errors.As(e, &domain) || len(s.saved) != 0 {
				t.Fatal(e, s.saved)
			}
			if kind == "compatibility" && domain.Code() != result.RecoveryRequired {
				t.Fatal(e)
			}
		})
	}
}

type probeFunc func(context.Context, target.Port, policy.Health) (bool, error)

func (f probeFunc) Check(c context.Context, p target.Port, h policy.Health) (bool, error) {
	return f(c, p, h)
}
func TestStatusProbeBounds(t *testing.T) {
	service, _, snap := fixture(t)
	active := target.Known(true)
	(*snap.Apps.Value)[0].UnitActive = &active
	service.Inventory = inventory{snap}
	calls := 0
	service.Probe = probeFunc(func(c context.Context, p target.Port, h policy.Health) (bool, error) {
		calls++
		deadline, ok := c.Deadline()
		if !ok || time.Until(deadline) > DirectProbeTimeout {
			t.Fatal("unbounded probe")
		}
		return false, errors.New("failed")
	})
	got, e := service.Status(context.Background(), "hello")
	if e != nil || calls != 1 || got.Apps[0].Health.Direct != "unhealthy" {
		t.Fatal(got, e, calls)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	got, e = service.Status(ctx, "hello")
	if e != nil || calls != 1 || got.Apps[0].Health.Direct != "not_checked" {
		t.Fatal(got, e, calls)
	}
}

func expectedRelease(id string) string {
	if id == "" {
		return "previous"
	}
	return id
}

func TestRollbackNoOpAndDriftedPlanning(t *testing.T) {
	for _, kind := range []string{"no_op", "drift"} {
		t.Run(kind, func(t *testing.T) {
			service, s, snap := fixture(t)
			if kind == "no_op" {
				current := s.state.Releases[0]
				s.previous.Image = current.Image
				s.desired["old-plan"] = current.Desired
				if _, e := service.Rollback(context.Background(), "hello", ""); result.Classify(e).Code() != result.RollbackNoOp || len(s.saved) != 0 {
					t.Fatal(e, s.saved)
				}
			} else {
				(*(*snap.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "sha256:" + strings.Repeat("f", 64)
				service.Inventory = inventory{snap}
				got, e := service.Rollback(context.Background(), "hello", "")
				if e != nil || got.Kind != plan.Conflict || len(s.saved) != 1 || len(s.saved[0].Changes) != 0 {
					t.Fatal(got, e)
				}
				encoded, e := json.Marshal(got)
				if e != nil {
					t.Fatal(e)
				}
				if _, e := DecodeRollback(encoded); e != nil {
					t.Fatal(e, string(encoded))
				}
			}
		})
	}
}

func TestStatusUnattributedLiveRoutesAreUnknownNotAppDrift(t *testing.T) {
	service, _, snap := fixture(t)
	active := target.Known(true)
	(*snap.Apps.Value)[0].UnitActive = &active
	snap.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{{Name: "file-" + strings.Repeat("b", 64), App: "", Domains: target.Known([]string{"hello.example.com", "unrelated.example.net"})}})
	service.Inventory = inventory{snap}
	calls := 0
	service.Probe = probeFunc(func(context.Context, target.Port, policy.Health) (bool, error) { calls++; return true, nil })
	got, e := service.Status(context.Background(), "hello")
	if e != nil {
		t.Fatal(e)
	}
	status := got.Apps[0]
	if status.Drift.State != "unknown" || len(status.Drift.Fields) != 0 || status.Health.Direct != "healthy" || calls != 1 {
		t.Fatalf("unattributed collector routes are not app drift: %+v; probes=%d", status, calls)
	}
}

func TestStatusOnlyComparesOwnAttributedRoutes(t *testing.T) {
	for _, own := range []string{"hello.example.com", "changed.example.com"} {
		t.Run(own, func(t *testing.T) {
			service, _, snap := fixture(t)
			snap.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{
				{Name: "hello.caddy", App: "hello", Domains: target.Known([]string{own})},
				{Name: "other.caddy", App: "other", Domains: target.Known([]string{"other.example.net"})},
				{Name: "file-" + strings.Repeat("b", 64), App: "", Domains: target.Known([]string{"unrelated.example.net"})},
			})
			service.Inventory = inventory{snap}
			got, e := service.Status(context.Background(), "hello")
			if e != nil {
				t.Fatal(e)
			}
			drift := got.Apps[0].Drift
			if own == "hello.example.com" {
				if drift.State != "in_sync" || len(drift.Fields) != 0 {
					t.Fatal(drift)
				}
			} else if drift.State != "drifted" || !slices.Equal(drift.Fields, []string{"domains"}) {
				t.Fatal(drift)
			}
		})
	}
}

func TestStatusManagedRouteMissingVersusUnobservable(t *testing.T) {
	for _, kind := range []string{"absent", "missing_file", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			service, _, snap := fixture(t)
			switch kind {
			case "absent":
				snap.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Absent}
			case "missing_file":
				snap.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: 4, Files: []target.CaddyFile{{Name: "other.caddy", Hash: "sha256:" + strings.Repeat("b", 64)}}})
			case "unknown":
				snap.CaddyConfig = target.Observation[target.CaddyConfigSet]{Status: target.Unknown}
			}
			service.Inventory = inventory{snap}
			got, e := service.Status(context.Background(), "hello")
			if e != nil {
				t.Fatal(e)
			}
			drift := got.Apps[0].Drift
			if kind == "unknown" {
				if drift.State != "unknown" || len(drift.Fields) != 0 {
					t.Fatal(drift)
				}
			} else if drift.State != "drifted" || !slices.Equal(drift.Fields, []string{"caddy"}) {
				t.Fatalf("missing managed route must be affirmative drift: %+v", drift)
			}
		})
	}
}
