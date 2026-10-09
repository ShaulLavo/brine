package host

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
)

func TestRecoveryFactoryBindsFreshOperationFacts(t *testing.T) {
	r := newDeployRig(t)
	planned := r.call(t, "plan", dispatch.PlanArgs{Spec: r.spec}).Data.(dispatch.Planned)
	p, d, err := r.store.LoadPlan(context.Background(), planned.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	recovery := r.server.Reconciler.(reconcile.Reconciler)
	first, err := recovery.ExecutorFor(context.Background(), ops.Operation{PlanID: planned.PlanID}, p, d)
	if err != nil {
		t.Fatal(err)
	}
	d.Name = "another-app"
	second, err := recovery.ExecutorFor(context.Background(), ops.Operation{PlanID: planned.PlanID}, p, d)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("shared mutable executor")
	}
	r.inventory.generationOffset = 1
	one, err := first.Facts.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Facts.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if one.Input.Desired.Name != "hello" || two.Input.Desired.Name != "another-app" || *one.Input.Snapshot.Generation.Value != 1 || *two.Input.Snapshot.Generation.Value != 1 {
		t.Fatal("factory used a different operation or cached facts")
	}
}
