package apply

import (
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
