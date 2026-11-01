package host

import (
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func TestUDPDirectHostRefusesWithoutAdapters(t *testing.T) {
	e := Executor{}
	for _, p := range []plan.Plan{{}, {UDP: &plan.UDPPlan{}}, {Removal: &plan.Removal{UDP: &plan.UDPReservation{}}}} {
		err := e.Run(t.Context(), "forged", p, policy.Desired{UDP: &policy.UDPDesired{}})
		if !errors.Is(err, plan.ErrInvalidUDPExecution) {
			t.Fatal("direct host bypass", err)
		}
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
		if !errors.Is(err, plan.ErrInvalidUDPExecution) {
			t.Fatalf("payload reached adapters: %v", err)
		}
	}
}
