//go:build linux

package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func inventoryRemoval(t *testing.T) (*Store, ops.Operation, Release) {
	t.Helper()
	s := openTest(t)
	ctx := context.Background()
	r := release(t, s, "initial-release")
	r.Units = r.Units[:1]
	if err := s.CommitRelease(ctx, "hello", r); err != nil {
		t.Fatal(err)
	}
	in := fixture(t)
	var err error
	_, in.Desired, err = s.LoadPlan(ctx, r.PlanID)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err = s.SavePlan(ctx, p, in.Desired); err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "fixture", "remove")
	if err != nil {
		t.Fatal(err)
	}
	return s, op, r
}

func advanceRemoval(t *testing.T, s *Store, id string) {
	t.Helper()
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Quiescing, ops.Starting, ops.Checking, ops.Committing} {
		if err := s.SetOperationState(context.Background(), id, state); err != nil {
			t.Fatal(err)
		}
	}
}

func assertInventoryApp(t *testing.T, s *Store, status target.Status, generation uint64) {
	t.Helper()
	state, err := s.InventoryState(context.Background())
	if err != nil || state.Generation != generation || len(state.Apps) != 1 || state.Apps[0].Name != "hello" || state.Apps[0].Status != status {
		t.Fatal(state, err)
	}
}

func TestInventoryAbsenceNeedsSettledRetirement(t *testing.T) {
	s, op, r := inventoryRemoval(t)
	assertInventoryApp(t, s, target.Unknown, 1)
	advanceRemoval(t, s, op.ID)
	if err := s.RetireApp(context.Background(), op.ID, "hello", r.ID); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.Unknown, 2)
	if err := s.SetOperationState(context.Background(), op.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.Absent, 2)
	state, _ := s.InventoryState(context.Background())
	if !reflect.DeepEqual(state.Apps[0].RetiredPorts, []target.Port{r.HostPort}) {
		t.Fatal("lost orphan-listener evidence", state)
	}
	read, err := ReadInventoryState(context.Background(), s.dir)
	if err != nil || !reflect.DeepEqual(state, read) {
		t.Fatal("restricted read differs", read, err)
	}
	r.ID = "replacement"
	if err := s.CommitRelease(context.Background(), "hello", r); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.KnownStatus, 3)
}

func TestInventoryAbsenceFollowsSuccessfulResolutionDescendants(t *testing.T) {
	s, source, r := inventoryRemoval(t)
	ctx := context.Background()
	advanceRemoval(t, s, source.ID)
	if err := s.RetireApp(ctx, source.ID, "hello", r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, source.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.Unknown, 2)
	child, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: source.PlanID, RecoveryOf: source.ID}, "fixture", "resolve-child")
	if err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.Unknown, 2)
	if err = s.SetOperationState(ctx, child.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	grandchild, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: source.PlanID, RecoveryOf: child.ID}, "fixture", "resolve-grandchild")
	if err != nil {
		t.Fatal(err)
	}
	advanceRemoval(t, s, grandchild.ID)
	assertInventoryApp(t, s, target.Unknown, 2)
	if err = s.SetOperationState(ctx, grandchild.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.Absent, 2)
	unchanged, err := s.GetOperation(ctx, source.ID)
	if err != nil || unchanged.State != ops.RecoveryRequired {
		t.Fatal("source receipt changed", unchanged, err)
	}
}

func TestInventoryDoesNotInventAbsenceFromRetainedHistory(t *testing.T) {
	s := openTest(t)
	r := release(t, s, "history")
	if err := s.CommitRelease(context.Background(), "hello", r); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.KnownStatus, 1)
	if _, err := s.db.Exec("DELETE FROM release_heads WHERE app='hello'"); err != nil {
		t.Fatal(err)
	}
	assertInventoryApp(t, s, target.Unknown, 1)
}

func TestReadInventoryRejectsUnreadableState(t *testing.T) {
	s := openTest(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InventoryState(context.Background()); err == nil {
		t.Fatal("closed state accepted")
	}
	if _, err := ReadInventoryState(context.Background(), filepath.Join(s.dir, "missing")); err == nil {
		t.Fatal("missing state accepted")
	}
}
