package host

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestManagedIntentProjectionDoesNotResumeAcrossEpochs(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	runtime := permit.Runtime
	fingerprint, err := managedRuntimeHash(runtime)
	if err != nil {
		t.Fatal(err)
	}
	wanted := store.ManagedDesiredState{BindingID: runtime.BindingID, EpochID: runtime.EpochID, RuntimeSHA256: fingerprint, Revision: 1, PlanID: "sha256:" + strings.Repeat("c", 64), OperationID: "op", EngineRunning: true, FrontendRunning: true, IngressServing: true}
	for _, different := range []bool{false, true} {
		current := runtime
		if different {
			current.EpochID = data.ReplicaEpochID(strings.Repeat("9", 32))
		}
		intent := &plan.ManagedIntentFacts{Revision: wanted.Revision}
		projectManagedIntent(intent, wanted, current)
		if intent.Current == different || intent.EngineRunning == different || intent.FrontendRunning == different || intent.IngressServing == different {
			t.Fatal("historical desired flags survived an epoch transition")
		}
	}
}
