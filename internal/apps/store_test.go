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
	db, e := store.Open(dir)
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
		if e := db.CommitRelease(ctx, "hello", release); e != nil {
			t.Fatal(e)
		}
	}
	service.Store = db
	report, e := service.Status(ctx, "hello")
	if e != nil {
		t.Fatal(e)
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
