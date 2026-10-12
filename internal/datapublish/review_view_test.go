package datapublish

import (
	"encoding/json"
	"testing"
)

func TestPublicModuleReviewViewKeepsActualCandidateAndRecoveryReferences(t *testing.T) {
	for _, create := range []bool{true, false} {
		plan, err := NewPlan(planFixture(t, create))
		if err != nil {
			t.Fatal(err)
		}
		view := plan.ReviewView()
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeReviewView(raw)
		if err != nil || decoded.PlanID != plan.ID() || decoded.SchemaSHA256 != plan.Candidate().Schema().SHA256() || decoded.Recovery.Kind() != plan.Review().Recovery.Kind() {
			t.Fatal("public review changed the retained private plan reference", err)
		}
		view.WasmSHA256 = "forged"
		if view.Valid() {
			t.Fatal("malformed public candidate accepted")
		}
	}
}
