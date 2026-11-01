package apply

import (
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func TestUDPIntentRefusesBeforeJournalOrEffects(t *testing.T) {
	e := &Executor{}
	err := e.Run(t.Context(), "", plan.Plan{}, policy.Desired{UDP: &policy.UDPDesired{}})
	if err == nil || !strings.Contains(err.Error(), "udp_execution_unavailable") {
		t.Fatalf("UDP bypass reached executor: %v", err)
	}
}

func TestUDPRecoveryAndResolutionRefuseBeforeAdapters(t *testing.T) {
	e := &Executor{}
	p := plan.Plan{UDP: &plan.UDPPlan{}}
	d := policy.Desired{}
	if _, err := e.InspectRecovery(t.Context(), Operation{}, p, d, nil); !errors.Is(err, plan.ErrUDPExecutionUnavailable) {
		t.Fatal("recovery bypass", err)
	}
	if _, err := e.InspectResolution(t.Context(), Operation{}, Operation{}, p, d, nil); !errors.Is(err, plan.ErrUDPExecutionUnavailable) {
		t.Fatal("resolution bypass", err)
	}
}

func TestHandcraftedUDPPayloadsRefuseBeforeEffects(t *testing.T) {
	plans := []plan.Plan{
		{UDP: &plan.UDPPlan{}},
		{Removal: &plan.Removal{UDP: &plan.UDPReservation{}}},
		{Changes: []plan.Change{{Kind: plan.ReserveUDP}}},
		{Changes: []plan.Change{{Kind: plan.ReleaseUDP}}},
		{Changes: []plan.Change{{UDP: &plan.UDPEndpoint{}}}},
		{Changes: []plan.Change{{Quadlet: &plan.Quadlet{UDP: &plan.UDPAllocation{}}}}},
		{Changes: []plan.Change{{Quadlet: &plan.Quadlet{Desired: policy.Desired{UDP: &policy.UDPDesired{}}}}}},
	}
	e := Executor{}
	for _, p := range plans {
		err := e.Run(t.Context(), "forged", p, policy.Desired{})
		if !errors.Is(err, plan.ErrUDPExecutionUnavailable) {
			t.Fatalf("payload reached adapters: %v", err)
		}
	}
}
