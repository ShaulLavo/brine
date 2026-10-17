package cdn

import (
	"bytes"
	"encoding/json"
	"testing"
)

func fixturePlan(t *testing.T) Plan {
	t.Helper()
	effect := fixtureEffect()
	sample, err := NewAssetSample(effect.Permit.Action.Attachment.Binding.Cache, "/objects/"+string(Hash([]byte("asset")))[7:]+".png")
	if err != nil {
		t.Fatal(err)
	}
	step := PlannedStep{Kind: effect.Permit.Action.Kind, Profile: effect.Permit.Action.Profile, ResourceKind: effect.Permit.Action.Resource.Kind, ExactName: effect.Permit.Action.Resource.ExactName, Desired: effect.Permit.Action.Desired, Precondition: InitialObservation{Before: effect.Permit.Action.Expected, Resource: effect.Permit.Action.Resource}}
	plan, err := NewPlan(PlanningContext{Attachment: effect.Permit.Action.Attachment, PolicyHash: Hash([]byte("policy"))}, sample, []PlannedStep{step}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestConnectedPlanHasCanonicalIdentityAndClosedSteps(t *testing.T) {
	plan := fixturePlan(t)
	encoded, err := plan.Encoded()
	if err != nil {
		t.Fatal(err)
	}
	var parsed Plan
	if json.Unmarshal(encoded, &parsed) != nil || parsed.Validate() != nil {
		t.Fatal("canonical plan did not round trip")
	}
	plan.Context.Attachment.Binding.Cache.Extensions = []string{"png", "ogg"}
	canonical, err := plan.Encoded()
	if err != nil || !bytes.Equal(canonical, encoded) {
		t.Fatal("unordered policy changed plan identity", err)
	}
	plan.Steps[0].Desired = Hash([]byte("another configuration"))
	if plan.Validate() == nil {
		t.Fatal("unreviewed action retained original plan identity")
	}
}

func TestEffectIdentityIncludesAdmittedOperation(t *testing.T) {
	plan := fixturePlan(t)
	action := fixtureEffect().Permit.Action
	action.Attachment = plan.Context.Attachment
	first, err := plan.NewPermit("operation-a", 0, action)
	if err != nil {
		t.Fatal(err)
	}
	second, err := plan.NewPermit("operation-b", 0, action)
	if err != nil || first.Effect == second.Effect {
		t.Fatal("separate admitted operations reused native effect identity", err)
	}
}
