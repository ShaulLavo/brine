package cdn

import "reflect"

type EffectStage string

const (
	Planned       EffectStage = "planned"
	Issued        EffectStage = "issued"
	Accepted      EffectStage = "accepted"
	Indeterminate EffectStage = "indeterminate"
	Confirmed     EffectStage = "confirmed"
	Rejected      EffectStage = "rejected"
)

type Effect struct {
	Permit     Permit          `json:"permit"`
	Stage      EffectStage     `json:"stage"`
	Receipt    Fingerprint     `json:"receipt"`
	Completion Fingerprint     `json:"completion"`
	Resources  []OwnedResource `json:"resources"`
}

// Claim is persisted by the host transaction before NeedEffect leaves the host.
// Every later request is inspect-only, including one carrying the original claim.
func (e Effect) Claim(claim ClaimID) (Effect, Work, error) {
	if claim == "" {
		return e, nil, ErrInvalid
	}
	if e.Stage != Planned {
		return e, e.Next(), nil
	}
	e.Stage, e.Permit.Claim = Issued, claim
	return e, NeedEffect{Permit: e.Permit}, nil
}

func (e Effect) Next() Work {
	switch e.Stage {
	case Planned:
		return Waiting{Code: "claim_required"}
	case Issued, Accepted, Indeterminate:
		return NeedObservation{Request: InspectIssued{Permit: e.Permit}}
	case Confirmed:
		return Finished{Operation: e.Permit.Operation, Revision: e.Permit.Action.Attachment.Revision, Succeeded: true}
	case Rejected:
		return Finished{Operation: e.Permit.Operation, Succeeded: false}
	default:
		return Waiting{Code: "invalid_effect_state"}
	}
}

func (e Effect) Record(result EffectResult) (Effect, error) {
	if e.Stage != Issued && e.Stage != Indeterminate && e.Stage != Accepted {
		return e, ErrConflict
	}
	if e.Stage == Accepted {
		value, ok := result.(Acknowledged)
		if ok && value.Effect == e.Permit.Effect && value.Receipt == e.Receipt && reflect.DeepEqual(value.Resources, e.Resources) {
			return e, nil
		}
		return e, ErrConflict
	}
	switch value := result.(type) {
	case Acknowledged:
		if value.Effect != e.Permit.Effect || !hashPattern.MatchString(string(value.Receipt)) {
			return e, ErrInvalid
		}
		e.Stage, e.Receipt, e.Resources = Accepted, value.Receipt, value.Resources
	case Refused:
		if value.Effect != e.Permit.Effect || value.Code == "" {
			return e, ErrInvalid
		}
		e.Stage = Rejected
	case Uncertain:
		if value.Effect != e.Permit.Effect || value.Code == "" {
			return e, ErrInvalid
		}
		e.Stage = Indeterminate
	default:
		return e, ErrInvalid
	}
	return e, nil
}

func (e Effect) Inspect(inspection Inspection) (Effect, Work, error) {
	if e.Stage != Issued && e.Stage != Accepted && e.Stage != Indeterminate {
		return e, nil, ErrConflict
	}
	switch value := inspection.(type) {
	case Observed:
		if value.Effect != e.Permit.Effect {
			return e, nil, ErrInvalid
		}
		if value.Relation != Desired || e.Stage != Accepted {
			return e, Waiting{Code: "effect_unsettled"}, nil
		}
		if err := e.checkResources(value.Current); err != nil {
			return e, nil, err
		}
	case Completed:
		if value.Effect != e.Permit.Effect || !hashPattern.MatchString(string(value.Evidence)) {
			return e, nil, ErrInvalid
		}
		if err := e.checkResources(value.Current); err != nil {
			return e, nil, err
		}
		e.Completion = value.Evidence
	default:
		return e, nil, ErrInvalid
	}
	e.Stage = Confirmed
	return e, e.Next(), nil
}

func (e Effect) checkResources(observed Observation) error {
	if observed.Profile != e.Permit.Action.Profile || observed.ObservedAt.IsZero() || observed.Fingerprint != e.Permit.Action.Desired {
		return ErrConflict
	}
	for _, resource := range observed.Resources {
		if resource.Owner != e.Permit.Action.Attachment.Owner {
			return ErrConflict
		}
	}
	for _, expected := range e.Resources {
		if !containsResource(observed.Resources, expected) {
			return ErrConflict
		}
	}
	return nil
}

func containsResource(resources []OwnedResource, expected OwnedResource) bool {
	for _, current := range resources {
		if current.Kind == expected.Kind && current.ID == expected.ID && current.ExactName == expected.ExactName && current.Owner == expected.Owner && current.Version == expected.Version && current.Fingerprint == expected.Fingerprint {
			return true
		}
	}
	return false
}
