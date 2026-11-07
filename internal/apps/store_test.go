package apps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestRollbackPersistsExactDesiredWithoutApplying(t *testing.T) {
	service, fake, snap := fixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := store.OpenContext(t.Context(), dir)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ready, e := os.ReadFile("../target/testdata/ready-arm64.json")
	if e != nil {
		t.Fatal(e)
	}
	empty, e := target.Decode(ready)
	if e != nil {
		t.Fatal(e)
	}
	current := fake.state.Releases[0]
	oldDesired := fake.desired["old-plan"]
	oldDesired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "PLANTED_PRIVATE_SETTING"}}
	oldDesired.Domains = []spec.Domain{"previous.example.com"}
	want := map[string]Release{}
	for _, r := range []struct {
		id      string
		desired policy.Desired
		image   plan.Image
	}{{"previous", oldDesired, fake.previous.Image}, {"current", current.Desired, current.Image}} {
		p, e := plan.Build(plan.Input{Desired: r.desired, Snapshot: empty, Image: r.image, State: plan.BrineState{Target: empty.Identity, Generation: *empty.Generation.Value, Releases: []plan.CurrentRelease{}}})
		if e != nil {
			t.Fatal(e)
		}
		id, e := db.SavePlan(ctx, p, r.desired)
		if e != nil {
			t.Fatal(e)
		}
		release := ops.Release{ID: r.id, PlanID: id, Image: r.image, HostPort: p.HostPort, Secrets: p.Secrets, Units: current.Units, CaddyFile: current.CaddyFile, CaddyGeneration: snap.CaddyConfig.Value.Generation}
		want[r.id] = Release{ID: r.id, PlanID: id, ImageDigest: r.image.Digest, Port: p.HostPort, Domains: r.desired.Domains}
		if e := db.CommitRelease(ctx, "hello", release); e != nil {
			t.Fatal(e)
		}
	}
	service.Store = db
	active := target.Known(true)
	(*snap.Apps.Value)[0].UnitActive = &active
	service.Inventory = inventory{snap}
	report, e := service.Status(ctx, "hello")
	if e != nil {
		t.Fatal(e)
	}
	if len(report.Apps) != 1 {
		t.Fatal(report)
	}
	item := report.Apps[0]
	currentView, previousView := want["current"], want["previous"]
	if !reflect.DeepEqual(item.Current, &currentView) || !reflect.DeepEqual(item.Previous, &previousView) || item.LastOperation != nil || item.Drift.State != "in_sync" || item.Health.Direct != "healthy" {
		t.Fatalf("real-store status metadata changed: %+v", item)
	}
	encoded, e := json.Marshal(report)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeReport(encoded); e != nil {
		t.Fatal(e, string(encoded))
	}
	planned, e := service.Rollback(ctx, "hello", "")
	if e != nil {
		t.Fatal(e)
	}
	p, desired, e := db.LoadPlan(ctx, planned.PlanID)
	if e != nil || p.Kind != plan.Update || !reflect.DeepEqual(desired, oldDesired) {
		t.Fatal(p.Kind, desired, e)
	}
	head, e := db.CurrentRelease(ctx, "hello")
	if e != nil || head.ID != "current" {
		t.Fatal(head, e)
	}
	if _, e := db.LastOperation(ctx, "hello"); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("planning created an operation", e)
	}
	encoded, e = json.Marshal(planned)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := DecodeRollback(encoded); e != nil {
		t.Fatal(e, string(encoded))
	}
	if strings.Contains(string(encoded), "PLANTED_PRIVATE_SETTING") {
		t.Fatal("setting leaked")
	}
	if planned.Diff.Environment == nil || len(planned.Diff.Environment.Added) != 1 {
		t.Fatal(planned.Diff)
	}
}

type failingStatusStateStore struct {
	Store
	err error
}

func (s failingStatusStateStore) LoadBrineState(context.Context, target.Identity, uint64) (plan.BrineState, error) {
	return plan.BrineState{}, s.err
}

func TestStatusCurrentStateFailuresKeepClassification(t *testing.T) {
	for _, failure := range []error{&store.IntegrityError{}, store.ErrNotFound, context.Canceled} {
		service, state, _ := fixture(t)
		service.Store = failingStatusStateStore{Store: state, err: failure}
		report, err := service.Status(context.Background(), "hello")
		if !errors.Is(err, failure) || result.Classify(err).Code() != result.Classify(failure).Code() || len(state.releaseReads) != 0 || len(report.Apps) != 0 {
			t.Fatalf("state failure changed: %v; reads %v; report %+v", err, state.releaseReads, report)
		}
	}
}
