package cdn

import (
	"encoding/json"
	"reflect"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type PreconditionSource interface{ preconditionSource() }
type InitialObservation struct {
	Before   Fingerprint
	Resource OwnedResource
}
type PriorOwnedReceipt struct{ StepIndex uint32 }

func (InitialObservation) preconditionSource() {}
func (PriorOwnedReceipt) preconditionSource()  {}

// A planned step binds identity and desired configuration, never a guessed
// precondition for a native resource that a prior write has yet to create.
type PlannedStep struct {
	Kind         ActionKind         `json:"kind"`
	Profile      ProfileScope       `json:"profile"`
	ResourceKind ResourceKind       `json:"resource_kind"`
	ExactName    string             `json:"exact_name"`
	Desired      Fingerprint        `json:"desired"`
	Precondition PreconditionSource `json:"precondition"`
}

type OwnedReceipt struct {
	Effect    EffectID        `json:"effect"`
	Profile   ProfileScope    `json:"profile"`
	Resources []OwnedResource `json:"resources"`
}

type SealedAction struct {
	Action  Action      `json:"action"`
	Current Observation `json:"current"`
	// The operator observes these through their original scoped drivers. A DNS
	// driver never receives authority to inspect another provider's edge account.
	Prerequisites  []Observation `json:"prerequisites"`
	OriginHealthy  bool          `json:"origin_healthy"`
	OriginEvidence Fingerprint   `json:"origin_evidence"`
}

type ObservationRequest interface{ observationRequest() }
type InspectIssued struct {
	Permit Permit `json:"permit"`
}
type PrepareStep struct {
	Operation  OperationID  `json:"operation"`
	StepIndex  uint32       `json:"step_index"`
	Attachment Attachment   `json:"attachment"`
	Step       PlannedStep  `json:"step"`
	Prior      OwnedReceipt `json:"prior"`
}
type NeedQualification struct {
	Qualification Qualification  `json:"qualification"`
	Receipts      []OwnedReceipt `json:"receipts"`
}

func (InspectIssued) observationRequest()     {}
func (PrepareStep) observationRequest()       {}
func (NeedQualification) observationRequest() {}

func (s InitialObservation) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind     string        `json:"kind"`
		Before   Fingerprint   `json:"before"`
		Resource OwnedResource `json:"resource"`
	}{"initial", s.Before, s.Resource})
}
func (s PriorOwnedReceipt) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Kind      string `json:"kind"`
		StepIndex uint32 `json:"step_index"`
	}{"prior_owned_receipt", s.StepIndex})
}

func (s *PlannedStep) UnmarshalJSON(data []byte) error {
	fields, err := strictjson.Object(data, "kind", "profile", "resource_kind", "exact_name", "desired", "precondition")
	if err != nil {
		return ErrInvalid
	}
	var plain struct {
		Kind         ActionKind      `json:"kind"`
		Profile      ProfileScope    `json:"profile"`
		ResourceKind ResourceKind    `json:"resource_kind"`
		ExactName    string          `json:"exact_name"`
		Desired      Fingerprint     `json:"desired"`
		Precondition json.RawMessage `json:"precondition"`
	}
	if json.Unmarshal(data, &plain) != nil {
		return ErrInvalid
	}
	precondition, err := decodePrecondition(fields["precondition"])
	if err != nil {
		return err
	}
	*s = PlannedStep{Kind: plain.Kind, Profile: plain.Profile, ResourceKind: plain.ResourceKind, ExactName: plain.ExactName, Desired: plain.Desired, Precondition: precondition}
	return nil
}

func decodePrecondition(data []byte) (PreconditionSource, error) {
	if fields, err := strictjson.Object(data, "kind", "before", "resource"); err == nil {
		kind, kindErr := strictjson.Value[string](fields["kind"])
		var source InitialObservation
		if kindErr != nil || kind != "initial" || json.Unmarshal(fields["before"], &source.Before) != nil || json.Unmarshal(fields["resource"], &source.Resource) != nil {
			return nil, ErrInvalid
		}
		return source, nil
	}
	fields, err := strictjson.Object(data, "kind", "step_index")
	if err != nil {
		return nil, ErrInvalid
	}
	kind, kindErr := strictjson.Value[string](fields["kind"])
	index, indexErr := strictjson.Value[uint32](fields["step_index"])
	if kindErr != nil || indexErr != nil || kind != "prior_owned_receipt" {
		return nil, ErrInvalid
	}
	return PriorOwnedReceipt{StepIndex: index}, nil
}

func (s PlannedStep) Validate(index uint32) error {
	if !s.Profile.Valid() || !hashPattern.MatchString(string(s.Desired)) || s.ExactName == "" || len(s.ExactName) > 256 || s.ResourceKind == "" || actionResourceKind(s.Kind) != s.ResourceKind {
		return ErrInvalid
	}
	switch source := s.Precondition.(type) {
	case InitialObservation:
		if !hashPattern.MatchString(string(source.Before)) || source.Resource.Kind != s.ResourceKind || source.Resource.ExactName != s.ExactName {
			return ErrInvalid
		}
	case PriorOwnedReceipt:
		if source.StepIndex >= index {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func actionResourceKind(kind ActionKind) ResourceKind {
	switch kind {
	case PutEdge, RemoveEdge:
		return EdgeConfiguration
	case SetEdgeExposure:
		return EdgeExposure
	case PutEdgeRoute, RemoveEdgeRoute:
		return EdgeRoute
	case SetTraffic:
		return HostnameTraffic
	default:
		return ""
	}
}

// ValidateSealed binds a read-only adapter proposal back to the fixed host plan.
// The host transaction performs this before saving a native intent and claim.
func ValidateSealed(attachment Attachment, step PlannedStep, sealed SealedAction, prior OwnedReceipt) error {
	action := sealed.Action
	if action.Kind != step.Kind || action.Profile != step.Profile || action.Resource.Kind != step.ResourceKind || action.Resource.ExactName != step.ExactName || action.Resource.Owner != attachment.Owner || action.Desired != step.Desired || !reflect.DeepEqual(action.Attachment, attachment) {
		return ErrConflict
	}
	if sealed.Current.Profile != step.Profile || sealed.Current.ObservedAt.IsZero() || action.Expected != sealed.Current.Fingerprint || !hashPattern.MatchString(string(action.Expected)) {
		return ErrConflict
	}
	if prior.Effect != "" && !verifiedPrerequisite(prior, sealed.Prerequisites) {
		return ErrConflict
	}
	switch source := step.Precondition.(type) {
	case InitialObservation:
		if action.Expected != source.Before || action.Resource.ID != source.Resource.ID || action.Resource.Version != source.Resource.Version {
			return ErrConflict
		}
	case PriorOwnedReceipt:
		if prior.Effect == "" || !prior.Profile.Valid() || !verifiedPrerequisite(prior, sealed.Prerequisites) {
			return ErrConflict
		}
	default:
		return ErrInvalid
	}
	return nil
}

func verifiedPrerequisite(prior OwnedReceipt, observations []Observation) bool {
	for _, observed := range observations {
		if observed.Profile == prior.Profile && !observed.ObservedAt.IsZero() && containsResources(observed.Resources, prior.Resources) {
			return true
		}
	}
	return false
}

func containsResources(actual, expected []OwnedResource) bool {
	if len(expected) == 0 {
		return false
	}
	for _, resource := range expected {
		if !containsResource(actual, resource) {
			return false
		}
	}
	return true
}
