//go:build linux

package store

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestManagedPortsReserveCommittedHeadWithoutInventingRuntimeAbsence(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	collector, runner, _, _, _ := inventoryLifecycleFixture(t, s)
	in := fixture(t)
	observed, err := collector.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot = observed
	in.State, err = s.LoadBrineState(ctx, observed.Identity, *observed.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	created, err := plan.Build(in)
	if err != nil || created.Kind != plan.Create {
		t.Fatal(created, err)
	}
	if _, err = s.SavePlan(ctx, created, in.Desired); err != nil {
		t.Fatal(err)
	}
	unit, err := quadlet.Render(in.Desired, created, *created.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	if created.HostPort != 20000 {
		t.Fatal("fixture selected a different initial port", created.HostPort)
	}
	site, err := caddy.CommittedSite(in.Desired, 20000)
	if err != nil {
		t.Fatal(err)
	}
	route, err := caddy.Render(site)
	if err != nil {
		t.Fatal(err)
	}
	head := Release{ID: "retained-head", PlanID: created.Hash, Image: created.Image, HostPort: created.HostPort, Secrets: created.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: "hello.caddy", Hash: digest(route)}}
	if err = s.CommitRelease(ctx, "hello", head); err != nil {
		t.Fatal(err)
	}
	before, err := s.CurrentRelease(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	runner["caddy version"] = "2.10.2"
	in.Snapshot, err = collector.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old := (*in.Snapshot.Apps.Value)[0]
	if old.AllocatedHostPort.Status != target.Unknown || !old.UnitsAbsent() || old.RuntimeAbsent() {
		t.Fatal("retained owner was called absent", old)
	}
	in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, *in.Snapshot.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../spec/testdata/managed.toml")
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../policy/testdata/managed-operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	in.Desired, err = policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ManagedPreview == nil || preview.ManagedPreview.EngineHostPort != created.HostPort+1 {
		t.Fatalf("retained head did not reserve its port for fresh allocation: %+v", preview.Conflicts)
	}
	after, err := s.CurrentRelease(ctx, "hello")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read-only reservation changed the canonical head", err)
	}
	global, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Reconcile}, "fixture", "pending-global-owner")
	if err != nil {
		t.Fatal(err)
	}
	blockedFacts, err := collector.Collect(ctx)
	if err != nil || (*blockedFacts.Apps.Value)[0].CommittedPortReservation != nil {
		t.Fatal("unfinished global mutation admitted", err)
	}
	if err := s.SetOperationState(ctx, global.ID, ops.Failed); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: created.Hash}, "fixture", "pending-old-owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []ops.State{ops.Queued, ops.Preflight, ops.RecoveryRequired} {
		advanceCommittedPortProbe(t, s, op.ID, state)
		in.Snapshot, err = collector.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, *in.Snapshot.Generation.Value)
		if err != nil {
			t.Fatal(err)
		}
		blocked, err := plan.Build(in)
		if err != nil || blocked.ManagedPreview == nil || blocked.ManagedPreview.EngineHostPort != 0 {
			t.Fatal("pending or unknown mutation lost its reservation refusal", state, err)
		}
	}

}

func advanceCommittedPortProbe(t *testing.T, s *Store, id string, state ops.State) {
	t.Helper()
	if state == ops.Queued {
		return
	}
	if err := s.SetOperationState(context.Background(), id, state); err != nil {
		t.Fatal(err)
	}
}
