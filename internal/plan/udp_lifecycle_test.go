package plan

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
)

func TestUDPDeploymentLifecycleEnabled(t *testing.T) {
	for _, existing := range []bool{false, true} {
		in := udpInput(t, existing)
		p := build(t, in)
		if p.Kind == Conflict {
			t.Fatalf("valid UDP deployment refused: %v", p.Conflicts)
		}
		if p.UDP == nil || p.UDP.Next == nil {
			t.Fatal("missing allocation")
		}
	}
}

func TestUDPResumePreparationIsOperationBound(t *testing.T) {
	in := udpInput(t, true)
	in.Desired.UDP.Grant.HostPorts = policy.PortRange{Min: 4434, Max: 4435}
	p := build(t, in)
	retired := *p.UDP.Retire
	retired.State, retired.Owner = "releasing", "operation"
	claim := UDPReservation{ID: "udp-operation", App: p.App, Incarnation: p.ResolvedIncarnation(), State: "staged", Owner: "operation", Endpoint: p.UDP.Next.Endpoint}
	in.State.UDP.Reservations = []UDPReservation{retired, claim}
	restored, prepared, err := RebuildInput(in, p, []string{"operation"})
	if err != nil || !prepared {
		t.Fatal(prepared, err)
	}
	rebuilt := build(t, restored)
	if rebuilt.Hash != p.Hash {
		t.Fatalf("preparation staled its own plan: %s != %s", rebuilt.Hash, p.Hash)
	}
	claim.Owner = "foreign"
	in.State.UDP.Reservations[1] = claim
	if _, _, err := RebuildInput(in, p, []string{"operation"}); err == nil {
		t.Fatal("foreign claim hidden")
	}
}

func TestUDPLifecycleUnsettledReservationRemainsNamedConflict(t *testing.T) {
	in := udpInput(t, true)
	in.State.UDP.Reservations[0].State = "releasing"
	in.State.UDP.Reservations[0].Owner = "foreign"
	p, err := BuildLifecycle(in, StartApp)
	if err != nil || p.Kind != Conflict {
		t.Fatal(p.Kind, err)
	}
	found := false
	for _, conflict := range p.Conflicts {
		if conflict.Code == UDPUnsettled {
			found = true
		}
	}
	if !found {
		t.Fatal(p.Conflicts)
	}
}
