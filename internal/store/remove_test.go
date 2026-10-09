//go:build linux

package store

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
	"testing"
)

func TestRetireAppPreservesHistoryAndReleasesPort(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	r := release(t, s, "initial-release")
	r.Units = r.Units[:1]
	if err := s.CommitRelease(ctx, "hello", r); err != nil {
		t.Fatal(err)
	}
	_, d, err := s.LoadPlan(ctx, r.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	in.Desired = d
	in.Snapshot.Generation = target.Known(uint64(1))
	in.State, err = s.LoadBrineState(ctx, in.Snapshot.Identity, 1)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot.Apps = target.Known([]target.App{{Name: "hello", Image: target.Known(target.Image{Digest: r.Image.Digest, Platform: r.Image.Platform}), AllocatedHostPort: target.Known(r.HostPort), QuadletUnits: target.Known(r.Units), Secrets: target.Known([]target.Secret{})}})
	in.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Generation: r.CaddyGeneration, Files: []target.CaddyFile{r.CaddyFile}})
	p, err := plan.BuildRemove(in)
	if err != nil || p.Kind != plan.Update {
		t.Fatal(p, err)
	}
	if _, err = s.SavePlan(ctx, p, d); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "requester", "remove")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RetireApp(ctx, op.ID, "hello", r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireApp(ctx, op.ID, "hello", r.ID); err != nil {
		t.Fatal("replay", err)
	}
	if _, err = s.CurrentRelease(ctx, "hello"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.ReleaseByID(ctx, "hello", r.ID); err != nil {
		t.Fatal("history", err)
	}
	state, err := s.LoadBrineState(ctx, in.Snapshot.Identity, 2)
	if err != nil || len(state.Releases) != 0 {
		t.Fatal(state, err)
	}
	n, err := s.Generation(ctx)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	removed, err := s.AppRetired(ctx, op.ID, "hello", r.ID)
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	r.ID = "replacement"
	if err = s.CommitRelease(ctx, "hello", r); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireApp(ctx, op.ID, "hello", "replacement"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old removal reached recreated app: %v", err)
	}
	if head, err := s.CurrentRelease(ctx, "hello"); err != nil || head.ID != "replacement" {
		t.Fatal(head, err)
	}
}
