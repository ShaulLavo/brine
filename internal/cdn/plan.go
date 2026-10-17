package cdn

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

const MaxSteps = 16

type PlanningContext struct {
	Attachment Attachment     `json:"attachment"`
	PolicyHash Fingerprint    `json:"policy_hash"`
	Previous   RevisionID     `json:"previous"`
	Retained   []OwnedReceipt `json:"retained"`
	Initial    []Observation  `json:"initial"`
	RecoveryOf OperationID    `json:"recovery_of"`
}

type Plan struct {
	ID      PlanID          `json:"id"`
	Context PlanningContext `json:"context"`
	Sample  AssetSample     `json:"sample"`
	Steps   []PlannedStep   `json:"steps"`
}

func (a Attachment) Validate() error {
	if a.Mode != Enabled && a.Mode != Direct || a.Binding.Validate() != nil || a.Owner.Target != a.Binding.Target || a.Owner.App != a.Binding.App || a.Owner.Incarnation == "" || a.Owner.Attachment == "" || a.Revision == "" || a.SourceRelease == "" {
		return ErrInvalid
	}
	for _, profile := range a.Binding.AllowedEdges {
		if a.Profile == profile {
			return nil
		}
	}
	return ErrInvalid
}

func NewPlan(context PlanningContext, sample AssetSample, edge, traffic []PlannedStep) (Plan, error) {
	if len(edge) > MaxSteps || len(traffic) > MaxSteps-len(edge) {
		return Plan{}, ErrInvalid
	}
	context = normalizedContext(context)
	steps := append([]PlannedStep{}, edge...)
	for index, step := range traffic {
		step, err := offsetTrafficStep(step, index, len(edge))
		if err != nil {
			return Plan{}, err
		}
		steps = append(steps, step)
	}
	p := Plan{Context: context, Sample: sample, Steps: steps}
	if err := p.validate(); err != nil {
		return Plan{}, err
	}
	canonical, err := p.CanonicalBytes()
	if err != nil {
		return Plan{}, err
	}
	p.ID = PlanID(Hash(canonical))
	return p, nil
}

func offsetTrafficStep(step PlannedStep, index, offset int) (PlannedStep, error) {
	source, ok := step.Precondition.(PriorOwnedReceipt)
	if !ok {
		return step, nil
	}
	if int(source.StepIndex) >= index {
		return PlannedStep{}, ErrInvalid
	}
	// #nosec G115 -- Both step lists are bounded by MaxSteps before rebasing.
	step.Precondition = PriorOwnedReceipt{StepIndex: source.StepIndex + uint32(offset)}
	return step, nil
}

func (p Plan) validate() error {
	if p.Context.Attachment.Validate() != nil || !hashPattern.MatchString(string(p.Context.PolicyHash)) || len(p.Steps) > MaxSteps {
		return ErrInvalid
	}
	if (Qualification{Attachment: p.Context.Attachment, Sample: p.Sample}).Validate() != nil {
		return ErrInvalid
	}
	for index, step := range p.Steps {
		if step.Validate(uint32(index)) != nil || !allowedStepProfile(p.Context.Attachment, step) {
			return ErrInvalid
		}
	}
	return nil
}

func allowedStepProfile(a Attachment, step PlannedStep) bool {
	if step.Kind == SetTraffic {
		return step.Profile == a.Binding.Traffic && step.ExactName == a.Binding.Hostname
	}
	return step.Profile == a.Profile
}

func (p Plan) CanonicalBytes() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	p.Context = normalizedContext(p.Context)
	p.ID = ""
	return json.Marshal(p)
}

func (p Plan) Validate() error {
	canonical, err := p.CanonicalBytes()
	if err != nil || p.ID != PlanID(Hash(canonical)) {
		return ErrInvalid
	}
	return nil
}

func (p Plan) NewPermit(operation OperationID, index uint32, sealed Action) (Permit, error) {
	if p.Validate() != nil || int(index) >= len(p.Steps) || operation == "" {
		return Permit{}, ErrInvalid
	}
	step := p.Steps[index]
	if step.Kind != sealed.Kind || step.Profile != sealed.Profile || step.ResourceKind != sealed.Resource.Kind || step.ExactName != sealed.Resource.ExactName || step.Desired != sealed.Desired || !reflect.DeepEqual(sealed.Attachment, p.Context.Attachment) {
		return Permit{}, ErrConflict
	}
	return Permit{Operation: operation, Effect: EffectID(Hash([]byte(string(operation) + "\n" + string(p.ID) + ":" + strconv.FormatUint(uint64(index), 10)))), Action: sealed}, nil
}

func (p Plan) Encoded() ([]byte, error) {
	if p.Validate() != nil {
		return nil, ErrInvalid
	}
	p.Context = normalizedContext(p.Context)
	return json.Marshal(p)
}

func normalizedContext(context PlanningContext) PlanningContext {
	if raw, err := context.Attachment.Binding.CanonicalBytes(); err == nil {
		binding, _ := ParseBinding(raw)
		context.Attachment.Binding = binding
	}
	context.Retained = slices.Clone(context.Retained)
	context.Initial = slices.Clone(context.Initial)
	for index := range context.Initial {
		context.Initial[index].Resources = normalizedResources(context.Initial[index].Resources)
	}
	slices.SortFunc(context.Initial, func(a, b Observation) int {
		return strings.Compare(string(a.Profile.Reference), string(b.Profile.Reference))
	})
	for index := range context.Retained {
		context.Retained[index].Resources = normalizedResources(context.Retained[index].Resources)
	}
	slices.SortFunc(context.Retained, func(a, b OwnedReceipt) int {
		return strings.Compare(string(a.Profile.Reference)+"\n"+string(a.Effect), string(b.Profile.Reference)+"\n"+string(b.Effect))
	})
	return context
}

func normalizedResources(resources []OwnedResource) []OwnedResource {
	resources = slices.Clone(resources)
	slices.SortFunc(resources, func(a, b OwnedResource) int {
		return strings.Compare(string(a.Kind)+"\n"+a.ExactName+"\n"+a.ID, string(b.Kind)+"\n"+b.ExactName+"\n"+b.ID)
	})
	return resources
}

func (p PublicProof) Validate(attachment Attachment, sample AssetSample) error {
	if p.Method != OperatorHTTP && p.Method != BrowserNetwork || p.Source != attachment.Profile || p.Sample != sample || p.Revision != attachment.Revision || p.ObservedAt.IsZero() || p.FullGETs < 2 || !p.HashesVerified || !p.OriginHealthy || !hashPattern.MatchString(string(p.Evidence)) {
		return ErrInvalid
	}
	if attachment.Mode == Enabled && (!p.EdgeHealthy || !p.LaterHit) {
		return ErrInvalid
	}
	if attachment.Mode == Direct && (p.EdgeHealthy || p.LaterHit) {
		return ErrInvalid
	}
	return nil
}

func RevisionFor(a Attachment, policy Fingerprint, previous RevisionID) RevisionID {
	a.Revision = ""
	encoded, _ := json.Marshal(a)
	return RevisionID(Hash(append(encoded, []byte("\n"+string(policy)+"\n"+string(previous))...)))
}

func AttachmentFor(target Fingerprint, app, sourceRelease string) Owner {
	identity := Hash([]byte(string(target) + "\n" + app + "\n" + sourceRelease))
	return Owner{Target: target, App: app, Incarnation: sourceRelease, Attachment: AttachmentID("cdn-" + strings.TrimPrefix(string(identity), "sha256:"))}
}
