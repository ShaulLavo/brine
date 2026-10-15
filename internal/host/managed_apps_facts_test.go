//go:build linux

package host

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedSnapshotInventory struct {
	calls    int
	snapshot target.Snapshot
}

func (i *managedSnapshotInventory) Collect(context.Context) (target.Snapshot, error) {
	i.calls++
	return i.snapshot, nil
}

type managedDeadlineImages struct {
	base     ImageResolver
	deadline time.Time
	calls    int
}

func (i *managedDeadlineImages) Resolve(ctx context.Context, ref spec.ImageReference, platform target.Platform) (plan.Image, error) {
	deadline, _ := ctx.Deadline()
	if deadline != i.deadline {
		panic("managed facts changed the parent deadline")
	}
	i.calls++
	return i.base.Resolve(ctx, ref, platform)
}

func TestManagedAppFactsReuseObservedSnapshotAndParentDeadline(t *testing.T) {
	rig, planned, _, _ := managedFactsRecoveryFixture(t)
	snapshot, err := rig.inventory.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state, err := rig.store.LoadBrineState(t.Context(), snapshot.Identity, *snapshot.Generation.Value)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err := rig.store.LoadPlan(t.Context(), planned.Hash)
	if err != nil {
		t.Fatal(err)
	}
	inventory := &managedSnapshotInventory{snapshot: snapshot}
	rig.service.Inventory = inventory
	parent, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	deadline, _ := parent.Deadline()
	images := &managedDeadlineImages{base: rig.service.Images, deadline: deadline}
	rig.service.Images = images
	input := plan.Input{Desired: desired, Snapshot: snapshot, State: state}
	actual, err := (managedAppFacts{service: rig.service}).ManagedFacts(parent, input)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.calls != 0 || images.calls != 2 || !reflect.DeepEqual(actual.Snapshot, snapshot) || !reflect.DeepEqual(actual.State, state) {
		t.Fatal("managed planning recollected or replaced observed authority", inventory.calls, images.calls)
	}
	input.State.Generation++
	if _, err := (managedAppFacts{service: rig.service}).ManagedFacts(parent, input); err == nil {
		t.Fatal("mismatched observed generation admitted")
	}
	if inventory.calls != 0 {
		t.Fatal("drift triggered replacement inventory")
	}
}
