//go:build linux

package host

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type udpProofSpy struct {
	calls      int
	planID     string
	allocation plan.UDPAllocation
	units      []target.Unit
}

func (s *udpProofSpy) UDPReady(_ context.Context, _ string, id string, a plan.UDPAllocation, units []target.Unit) error {
	s.calls++
	s.planID, s.allocation, s.units = id, a, units
	return nil
}
func (s *udpProofSpy) UDPAbsent(context.Context, string, plan.UDPEndpoint) error {
	s.calls++
	return nil
}

type udpRebuildInventory struct {
	*fakeInventory
	calls int
}

func (i *udpRebuildInventory) RebuildUDP(ctx context.Context, _ plan.Plan, _ []string) (target.Snapshot, error) {
	i.calls++
	return i.Collect(ctx)
}

func hostUDPPlan(t *testing.T) (*deployRig, plan.Plan, policy.Desired, ops.Operation) {
	t.Helper()
	r := newDeployRig(t)
	raw, err := os.ReadFile("../policy/testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io"))
	raw = append(raw, []byte("\n[[udp_grants]]\napp = \"hello\"\nexposure = \"public\"\nbind_address = \"192.0.2.10\"\nclient_address = \"relay.example.test\"\nhost_ports = { min = 4433, max = 4435 }\n")...)
	r.policy.p, err = policy.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.inventory.snapshot.LocalAddresses = target.Known([]string{"192.0.2.10"})
	app, err := spec.Parse([]byte(r.spec + "\n[udp]\ncontainer_port = 4433\nexposure = \"public\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	planned, err := r.service.Plan(t.Context(), app)
	if err != nil || planned.Kind == plan.Conflict {
		t.Fatal(planned.Conflicts, err)
	}
	p, d, err := r.store.LoadPlan(t.Context(), planned.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := r.store.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "udp-test", "host")
	if err != nil {
		t.Fatal(err)
	}
	return r, p, d, op
}
func TestUDPPhysicalProofBindsPreparedFamilyPlanAndNewUnit(t *testing.T) {
	r, p, d, op := hostUDPPlan(t)
	spy := &udpProofSpy{}
	proof := udpProof{store: r.store, inventory: spy}
	if err := proof.Ready(t.Context(), op.ID, p, false); err == nil || spy.calls != 0 {
		t.Fatal("unprepared physical proof admitted", err)
	}
	if err := r.store.PrepareUDP(t.Context(), op.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := proof.Ready(t.Context(), op.ID, p, false); err != nil {
		t.Fatal(err)
	}
	expected, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	if spy.planID != p.Hash || spy.allocation != *p.UDP.Next || len(spy.units) != 1 || spy.units[0].Hash != expected.Hash() {
		t.Fatal("physical proof was not bound to newly rendered immutable intent")
	}
	state, err := r.store.UDPPreparation(t.Context(), op.ID, p)
	if err != nil || state != "prepared" {
		t.Fatal("proof activated reservation", state, err)
	}
	forged := p
	intent := *p.UDP
	allocation := *p.UDP.Next
	allocation.ContainerPort++
	intent.Next = &allocation
	forged.UDP = &intent
	if err := proof.Ready(t.Context(), op.ID, forged, false); err == nil || spy.calls != 1 {
		t.Fatal("mutated allocation reached physical proof", err)
	}
	outsider, _, err := r.store.CreateOperation(t.Context(), ops.Intent{Kind: ops.Deploy, PlanID: p.Hash}, "udp-test", "outsider")
	if err != nil {
		t.Fatal(err)
	}
	if err := proof.Ready(t.Context(), outsider.ID, p, false); err == nil || spy.calls != 1 {
		t.Fatal("foreign operation adopted prepared proof", err)
	}
}
func TestUDPHostAndApplyPreflightBothRebuildOwnPreparation(t *testing.T) {
	r, p, d, op := hostUDPPlan(t)
	if err := r.store.PrepareUDP(t.Context(), op.ID, p); err != nil {
		t.Fatal(err)
	}
	inventory := &udpRebuildInventory{fakeInventory: r.inventory}
	service := r.service
	service.Inventory = inventory
	host := r.runner.Executor.(Executor)
	host.Service = service
	host.Engine.Routes = fakeRoutes{r.policy.p}
	host.Engine.UDPReservations = r.store
	host.Engine.UDPChecks = udpProof{store: r.store, inventory: &udpProofSpy{}}
	if err := host.Run(t.Context(), op.ID, p, d); err != nil {
		t.Fatal(err)
	}
	if inventory.calls < 2 {
		t.Fatal("a freshness comparison skipped operation-bound rebuild", inventory.calls)
	}
	state, err := r.store.UDPPreparation(t.Context(), op.ID, p)
	if err != nil || state != "committed" {
		t.Fatal(state, err)
	}
}
