package cdnoperator

import (
	"context"
	"reflect"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type Coordinator struct {
	Host        Host
	Drivers     Drivers
	OriginCheck func(context.Context, cdn.Qualification) (cdn.Fingerprint, error)
}

func (c Coordinator) Plan(ctx context.Context, request PlanRequest) (cdn.Plan, error) {
	if c.Host == nil || c.Drivers == nil || !request.Valid() {
		return cdn.Plan{}, cdn.ErrInvalid
	}
	planning, err := c.Host.Context(ctx, request)
	if err != nil {
		return cdn.Plan{}, err
	}
	a := planning.Context.Attachment
	if a.Validate() != nil || a.Owner.App != request.App || a.Binding.Reference != request.Binding || a.Profile.Reference != request.Profile || a.Mode != request.Mode || planning.Context.RecoveryOf != request.RecoverOperation {
		return cdn.Plan{}, cdn.ErrConflict
	}
	if sample, err := cdn.NewAssetSample(a.Binding.Cache, request.SamplePath); err != nil || sample != planning.Sample {
		return cdn.Plan{}, cdn.ErrConflict
	}
	edge, err := c.Drivers.Edge(a.Profile)
	if err != nil {
		return cdn.Plan{}, err
	}
	traffic, err := c.Drivers.Traffic(a.Binding.Traffic)
	if err != nil {
		return cdn.Plan{}, err
	}
	beforeEdge, err := edge.Observe(ctx, a)
	if err != nil {
		return cdn.Plan{}, err
	}
	beforeTraffic, err := traffic.Observe(ctx, a)
	if err != nil {
		return cdn.Plan{}, err
	}
	edgeSteps, err := edge.Actions(a, beforeEdge)
	if err != nil {
		return cdn.Plan{}, err
	}
	trafficSteps, err := traffic.Actions(a, beforeTraffic)
	if err != nil {
		return cdn.Plan{}, err
	}
	planning.Context.Initial = []cdn.Observation{beforeEdge, beforeTraffic}
	p, err := cdn.NewPlan(planning.Context, planning.Sample, edgeSteps, trafficSteps)
	if err != nil {
		return cdn.Plan{}, err
	}
	saved, err := c.Host.SavePlan(ctx, p)
	if err == nil && (saved.Validate() != nil || saved.ID != p.ID) {
		return cdn.Plan{}, cdn.ErrConflict
	}
	return saved, err
}

func (c Coordinator) Apply(ctx context.Context, request ApplyRequest) (Progress, error) {
	if c.Host == nil || !request.Valid() {
		return Progress{}, cdn.ErrInvalid
	}
	progress, err := c.Host.Apply(ctx, request)
	if err != nil {
		return progress, err
	}
	return c.run(ctx, progress)
}

func (c Coordinator) Resume(ctx context.Context, operation cdn.OperationID) (Progress, error) {
	if c.Host == nil || !ValidOperation(operation) {
		return Progress{}, cdn.ErrInvalid
	}
	progress, err := c.Host.Next(ctx, OperationRequest{Operation: operation})
	if err != nil {
		return progress, err
	}
	return c.run(ctx, progress)
}

func (c Coordinator) run(ctx context.Context, progress Progress) (Progress, error) {
	for turn := 0; turn < cdn.MaxSteps*4+4; turn++ {
		if ctx.Err() != nil {
			return progress, ctx.Err()
		}
		if err := progress.Validate(); err != nil {
			return progress, err
		}
		next, stop, err := c.advance(ctx, progress)
		if err != nil && next.Operation == "" {
			return progress, err
		}
		if err != nil || stop {
			return next, err
		}
		progress = next
	}
	return progress, cdn.ErrConflict
}

func (c Coordinator) advance(ctx context.Context, progress Progress) (Progress, bool, error) {
	switch work := progress.Work.(type) {
	case cdn.NeedEffect:
		next, err := c.execute(ctx, progress, work.Permit)
		return next, false, err
	case cdn.NeedObservation:
		return c.observeWork(ctx, progress, work.Request)
	case cdn.Waiting, cdn.Finished:
		return progress, true, nil
	default:
		return progress, true, cdn.ErrInvalid
	}
}

func (c Coordinator) observeWork(ctx context.Context, progress Progress, request cdn.ObservationRequest) (Progress, bool, error) {
	switch work := request.(type) {
	case cdn.PrepareStep:
		next, err := c.prepare(ctx, progress, work)
		return next, false, err
	case cdn.InspectIssued:
		next, err := c.inspect(ctx, progress, work.Permit)
		return next, false, err
	case cdn.NeedQualification:
		next, err := c.qualify(ctx, progress, work)
		return next, false, err
	default:
		return progress, true, cdn.ErrInvalid
	}
}

type effectDriver interface {
	Seal(context.Context, cdn.Attachment, cdn.PlannedStep, cdn.OwnedReceipt) (cdn.SealedAction, error)
	Execute(context.Context, cdn.Permit) (cdn.EffectResult, error)
	Inspect(context.Context, cdn.Permit) (cdn.Inspection, error)
}

func (c Coordinator) driver(a cdn.Attachment, profile cdn.ProfileScope, kind cdn.ActionKind) (effectDriver, error) {
	if c.Drivers == nil {
		return nil, cdn.ErrInvalid
	}
	if kind == cdn.SetTraffic && profile == a.Binding.Traffic {
		return c.Drivers.Traffic(profile)
	}
	if kind != cdn.SetTraffic && profile == a.Profile {
		return c.Drivers.Edge(profile)
	}
	return nil, cdn.ErrConflict
}

func (c Coordinator) prepare(ctx context.Context, p Progress, request cdn.PrepareStep) (Progress, error) {
	driver, err := c.driver(request.Attachment, request.Step.Profile, request.Step.Kind)
	if err != nil {
		return p, err
	}
	sealed, err := driver.Seal(ctx, request.Attachment, request.Step, request.Prior)
	if err != nil {
		return p, err
	}
	if request.Prior.Effect != "" {
		observed, err := c.observeProfile(ctx, request.Prior.Attachment, request.Prior.Profile, trafficReceipt(request.Prior))
		if err != nil {
			return p, err
		}
		sealed.Prerequisites = append(sealed.Prerequisites, observed)
	}
	if request.Step.Kind == cdn.SetTraffic {
		sealed, err = c.originReady(ctx, p.Plan, sealed)
		if err != nil {
			return p, err
		}
	}
	if cdn.ValidateSealed(request.Attachment, request.Step, sealed, request.Prior) != nil {
		return p, cdn.ErrConflict
	}
	return c.Host.Seal(ctx, SealRequest{Operation: p.Operation, StepIndex: request.StepIndex, Sealed: sealed})
}

func (c Coordinator) originReady(ctx context.Context, p cdn.Plan, sealed cdn.SealedAction) (cdn.SealedAction, error) {
	check := c.OriginCheck
	if check == nil {
		check = VerifyOrigin
	}
	evidence, err := check(ctx, cdn.Qualification{Attachment: p.Context.Attachment, Sample: p.Sample})
	if err != nil {
		return sealed, err
	}
	sealed.OriginHealthy, sealed.OriginEvidence = true, evidence
	return sealed, nil
}

func (c Coordinator) execute(ctx context.Context, p Progress, permit cdn.Permit) (Progress, error) {
	driver, err := c.driver(permit.Action.Attachment, permit.Action.Profile, permit.Action.Kind)
	if err != nil {
		return p, err
	}
	report, err := driver.Execute(ctx, permit)
	if err != nil || report == nil {
		report = cdn.Uncertain{Effect: permit.Effect, Code: "operator_effect_unsettled"}
	}
	return c.Host.RecordResult(ctx, ResultRequest{Operation: p.Operation, Result: report})
}

func (c Coordinator) inspect(ctx context.Context, p Progress, permit cdn.Permit) (Progress, error) {
	driver, err := c.driver(permit.Action.Attachment, permit.Action.Profile, permit.Action.Kind)
	if err != nil {
		return p, err
	}
	report, err := driver.Inspect(ctx, permit)
	if err != nil {
		return p, err
	}
	return c.Host.RecordInspection(ctx, InspectionRequest{Operation: p.Operation, Inspection: report})
}

func (c Coordinator) qualify(ctx context.Context, p Progress, request cdn.NeedQualification) (Progress, error) {
	edge, err := c.Drivers.Edge(request.Qualification.Attachment.Profile)
	if err != nil {
		return p, err
	}
	proof, err := edge.Probe(ctx, request.Qualification)
	if err != nil {
		return p, err
	}
	if proof.Validate(p.Plan.Context.Attachment, p.Plan.Sample) != nil {
		return p, cdn.ErrConflict
	}
	readback, err := c.readback(ctx, p.Plan.Context.Attachment, request.Receipts)
	if err != nil {
		return p, err
	}
	return c.Host.Qualify(ctx, ProofRequest{Operation: p.Operation, Proof: proof, Readback: readback})
}

func (c Coordinator) readback(ctx context.Context, a cdn.Attachment, receipts []cdn.OwnedReceipt) ([]cdn.Observation, error) {
	type readTarget struct {
		receipt cdn.OwnedReceipt
		traffic bool
	}
	targets := []readTarget{{receipt: cdn.OwnedReceipt{Profile: a.Profile, Attachment: a}}, {receipt: cdn.OwnedReceipt{Profile: a.Binding.Traffic, Attachment: a}, traffic: true}}
	for _, receipt := range receipts {
		targets = append(targets, readTarget{receipt: receipt, traffic: trafficReceipt(receipt)})
	}
	var observations []cdn.Observation
	type key struct {
		Profile  cdn.ProfileScope
		Revision cdn.RevisionID
		Traffic  bool
	}
	seen := make(map[key]bool)
	for _, target := range targets {
		identity := key{Profile: target.receipt.Profile, Revision: target.receipt.Attachment.Revision, Traffic: target.traffic}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		observed, err := c.observeProfile(ctx, target.receipt.Attachment, target.receipt.Profile, target.traffic)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observed)
	}
	return observations, nil
}

func trafficReceipt(receipt cdn.OwnedReceipt) bool {
	return len(receipt.Resources) > 0 && receipt.Resources[0].Kind == cdn.HostnameTraffic
}

func (c Coordinator) observeProfile(ctx context.Context, a cdn.Attachment, profile cdn.ProfileScope, traffic bool) (cdn.Observation, error) {
	if traffic {
		driver, err := c.Drivers.Traffic(profile)
		if err != nil {
			return cdn.Observation{}, err
		}
		return driver.Observe(ctx, a)
	}
	driver, err := c.Drivers.Edge(profile)
	if err != nil {
		return cdn.Observation{}, err
	}
	a.Profile = profile
	return driver.Observe(ctx, a)
}

func (c Coordinator) Status(ctx context.Context, request StatusRequest, refresh bool) (Status, error) {
	if c.Host == nil || !request.Valid() || refresh && c.Drivers == nil {
		return Status{}, cdn.ErrInvalid
	}
	status, err := c.Host.Status(ctx, request)
	if err != nil || !refresh {
		return status, err
	}
	if status.Plan == nil {
		return status, nil
	}
	p := *status.Plan
	if p.Validate() != nil || p.Context.Attachment.Owner.App != request.App {
		return status, cdn.ErrConflict
	}
	status.Observation, err = c.readback(ctx, p.Context.Attachment, p.Context.Retained)
	if err != nil {
		return status, err
	}
	edge, err := c.Drivers.Edge(p.Context.Attachment.Profile)
	if err != nil {
		return status, err
	}
	proof, err := edge.Probe(ctx, cdn.Qualification{Attachment: p.Context.Attachment, Sample: p.Sample})
	if err != nil {
		return status, err
	}
	if proof.Source != p.Context.Attachment.Profile || proof.Sample != p.Sample || proof.Revision != p.Context.Attachment.Revision || proof.ObservedAt.IsZero() {
		return status, cdn.ErrConflict
	}
	status.Proof = &proof
	return status, nil
}

func (p Progress) Validate() error {
	if !ValidOperation(p.Operation) || p.Plan.Validate() != nil {
		return cdn.ErrInvalid
	}
	switch work := p.Work.(type) {
	case cdn.NeedEffect:
		return p.validatePermit(work.Permit)
	case cdn.NeedObservation:
		return p.validateRequest(work.Request)
	case cdn.Waiting:
		if work.Code == "" || len(work.Code) > 128 {
			return cdn.ErrInvalid
		}
	case cdn.Finished:
		if work.Operation != p.Operation || work.Succeeded && work.Revision != p.Plan.Context.Attachment.Revision {
			return cdn.ErrConflict
		}
	default:
		return cdn.ErrInvalid
	}
	return nil
}

func (p Progress) validatePermit(permit cdn.Permit) error {
	if permit.Operation != p.Operation || permit.Claim == "" {
		return cdn.ErrConflict
	}
	for index := range p.Plan.Steps {
		want, err := p.Plan.NewPermit(p.Operation, uint32(index), permit.Action)
		if err == nil && want.Effect == permit.Effect {
			return nil
		}
	}
	return cdn.ErrConflict
}

func (p Progress) validateRequest(request cdn.ObservationRequest) error {
	switch value := request.(type) {
	case cdn.InspectIssued:
		return p.validatePermit(value.Permit)
	case cdn.PrepareStep:
		if value.Operation != p.Operation || int(value.StepIndex) >= len(p.Plan.Steps) || !reflect.DeepEqual(value.Attachment, p.Plan.Context.Attachment) || !reflect.DeepEqual(value.Step, p.Plan.Steps[value.StepIndex]) {
			return cdn.ErrConflict
		}
	case cdn.NeedQualification:
		if !reflect.DeepEqual(value.Qualification, cdn.Qualification{Attachment: p.Plan.Context.Attachment, Sample: p.Plan.Sample}) {
			return cdn.ErrConflict
		}
	default:
		return cdn.ErrInvalid
	}
	return nil
}
