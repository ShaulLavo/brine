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
		if !errors.Is(err, plan.ErrUDPExecutionUnavailable) {
			t.Fatal("direct host bypass", err)
		}
	}
}
