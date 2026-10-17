package cdn

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestConfirmedResourceVersionCannotDrift(t *testing.T) {
	effect, _, _ := fixtureEffect().Claim("writer")
	resource := effect.Permit.Action.Resource
	resource.ID, resource.Version = "native-id", "version-1"
	accepted, err := effect.Record(Acknowledged{Effect: effect.Permit.Effect, Receipt: Hash([]byte("ack")), Resources: []OwnedResource{resource}})
	if err != nil {
		t.Fatal(err)
	}
	resource.Version = "version-2"
	current := Observation{Profile: effect.Permit.Action.Profile, Resources: []OwnedResource{resource}, Fingerprint: effect.Permit.Action.Desired, ObservedAt: time.Now().UTC()}
	if _, _, err := accepted.Inspect(Observed{Effect: effect.Permit.Effect, Current: current, Relation: Desired}); err == nil {
		t.Fatal("changed native version silently accepted")
	}
}

func TestPlannedPreconditionsAreClosedAndCannotDependOnThemselves(t *testing.T) {
	effect := fixtureEffect()
	step := PlannedStep{Kind: PutEdge, Profile: effect.Permit.Action.Profile, ResourceKind: EdgeConfiguration, ExactName: "owned-edge", Desired: effect.Permit.Action.Desired, Precondition: InitialObservation{Before: Hash(nil), Resource: effect.Permit.Action.Resource}}
	wire, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	var parsed PlannedStep
	if json.Unmarshal(wire, &parsed) != nil || parsed.Validate(0) != nil {
		t.Fatal("canonical planned step did not round trip")
	}
	duplicate := bytes.Replace(wire, []byte(`"kind":"initial"`), []byte(`"kind":"initial","kind":"initial"`), 1)
	if json.Unmarshal(duplicate, &parsed) == nil {
		t.Fatal("duplicate precondition tag accepted")
	}
	step.Precondition = PriorOwnedReceipt{StepIndex: 0}
	if step.Validate(0) == nil || step.Validate(1) != nil {
		t.Fatal("dependency is not strictly earlier")
	}
}

func TestFreshSealingCannotChangeFixedDesiredOrNativeVersion(t *testing.T) {
	effect := fixtureEffect()
	attachment := effect.Permit.Action.Attachment
	prior := effect.Permit.Action.Resource
	prior.ID, prior.Version = "script-id", "version-1"
	source := OwnedReceipt{Effect: "prior-effect", Profile: effect.Permit.Action.Profile, Resources: []OwnedResource{prior}}
	step := PlannedStep{Kind: SetEdgeExposure, Profile: effect.Permit.Action.Profile, ResourceKind: EdgeExposure, ExactName: "owned-edge", Desired: Hash([]byte("private-exposure")), Precondition: PriorOwnedReceipt{StepIndex: 0}}
	action := Action{Kind: step.Kind, Attachment: attachment, Profile: step.Profile, Resource: OwnedResource{Kind: step.ResourceKind, ExactName: step.ExactName, Owner: attachment.Owner}, Expected: Hash([]byte("fresh-native-default")), Desired: step.Desired}
	current := Observation{Profile: step.Profile, Fingerprint: action.Expected, ObservedAt: time.Now().UTC()}
	prerequisite := Observation{Profile: source.Profile, Resources: []OwnedResource{prior}, ObservedAt: time.Now().UTC()}
	sealed := SealedAction{Action: action, Current: current, Prerequisites: []Observation{prerequisite}}
	if ValidateSealed(attachment, step, sealed, source) != nil {
		t.Fatal("fresh owned dependent action refused")
	}
	sealed.Prerequisites[0].Resources[0].Version = "version-2"
	if ValidateSealed(attachment, step, sealed, source) == nil {
		t.Fatal("prior native version drift accepted")
	}
	sealed.Prerequisites[0].Resources[0].Version = "version-1"
	sealed.Action.Desired = Hash([]byte("unreviewed"))
	if ValidateSealed(attachment, step, sealed, source) == nil {
		t.Fatal("sealed action changed reviewed desired state")
	}
}
