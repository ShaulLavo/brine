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
