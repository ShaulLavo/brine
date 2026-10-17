package cloudflare

import (
	"context"
	"net"
	"net/url"

	"github.com/ShaulLavo/brine/internal/cdn"
)

var _ cdn.EdgeDriver = (*Adapter)(nil)
var _ cdn.TrafficDriver = (*Adapter)(nil)

func (d *Adapter) Actions(a cdn.Attachment, before cdn.Observation) ([]cdn.PlannedStep, error) {
	if err := d.admit(a); err != nil {
		return nil, err
	}
	if before.Profile != d.scope || before.ObservedAt.IsZero() {
		return nil, cdn.ErrInvalid
	}
	if d.traffic {
		return d.trafficSteps(a, before)
	}
	if a.Mode == cdn.Direct {
		return d.directSteps(a, before)
	}
	return d.edgeSteps(a, before)
}

func (d *Adapter) edgeSteps(a cdn.Attachment, before cdn.Observation) ([]cdn.PlannedStep, error) {
	var steps []cdn.PlannedStep
	for _, kind := range []cdn.ResourceKind{cdn.EdgeConfiguration, cdn.EdgeExposure, cdn.EdgeRoute} {
		current := selected(before, kind)
		if len(current.Resources) != 1 {
			return nil, cdn.ErrInvalid
		}
		desired, err := d.desiredResource(a, kind)
		if err != nil {
			return nil, err
		}
		if current.Fingerprint == desired.Fingerprint {
			continue
		}
		if kind == cdn.EdgeConfiguration && current.Resources[0].ID != "" {
			return nil, cdn.ErrConflict
		}
		steps = append(steps, d.plannedStep(edgeAction(kind), desired, current, steps))
	}
	return steps, nil
}

func edgeAction(kind cdn.ResourceKind) cdn.ActionKind {
	switch kind {
	case cdn.EdgeConfiguration:
		return cdn.PutEdge
	case cdn.EdgeExposure:
		return cdn.SetEdgeExposure
	case cdn.EdgeRoute:
		return cdn.PutEdgeRoute
	default:
		return ""
	}
}

func (d *Adapter) plannedStep(kind cdn.ActionKind, desired cdn.OwnedResource, current cdn.Observation, prior []cdn.PlannedStep) cdn.PlannedStep {
	var precondition cdn.PreconditionSource = cdn.InitialObservation{Before: current.Fingerprint, Resource: current.Resources[0]}
	if len(prior) > 0 {
		precondition = cdn.PriorOwnedReceipt{StepIndex: uint32(len(prior) - 1)} // #nosec G115 -- edgeSteps generates at most three steps; other callers pass nil.
	}
	return cdn.PlannedStep{Kind: kind, Profile: d.scope, ResourceKind: desired.Kind, ExactName: desired.ExactName, Desired: desired.Fingerprint, Precondition: precondition}
}

func (d *Adapter) directSteps(a cdn.Attachment, before cdn.Observation) ([]cdn.PlannedStep, error) {
	current := selected(before, cdn.EdgeRoute)
	if len(current.Resources) != 1 {
		return nil, cdn.ErrInvalid
	}
	if current.Resources[0].ID == "" {
		return nil, nil
	}
	desired, err := d.desiredResource(a, cdn.EdgeRoute)
	if err != nil {
		return nil, err
	}
	return []cdn.PlannedStep{d.plannedStep(cdn.RemoveEdgeRoute, desired, current, nil)}, nil
}

func (d *Adapter) trafficSteps(a cdn.Attachment, before cdn.Observation) ([]cdn.PlannedStep, error) {
	current := selected(before, cdn.HostnameTraffic)
	if len(current.Resources) != 1 {
		return nil, cdn.ErrInvalid
	}
	desired, err := d.desiredTraffic(a)
	if err != nil {
		return nil, err
	}
	if current.Fingerprint == desired.Fingerprint {
		return nil, nil
	}
	return []cdn.PlannedStep{d.plannedStep(cdn.SetTraffic, desired, current, nil)}, nil
}

func (d *Adapter) desiredTraffic(a cdn.Attachment) (cdn.OwnedResource, error) {
	origin, err := url.Parse(a.Binding.Origin.Address)
	if err != nil {
		return cdn.OwnedResource{}, cdn.ErrInvalid
	}
	typeName := "CNAME"
	if ip := net.ParseIP(origin.Hostname()); ip != nil {
		typeName = "AAAA"
		if ip.To4() != nil {
			typeName = "A"
		}
	}
	record := dnsRecord{Type: typeName, Name: a.Binding.Hostname, Content: origin.Hostname(), Proxied: a.Mode == cdn.Enabled}
	return cdn.OwnedResource{Kind: cdn.HostnameTraffic, ID: d.client.profile.document.DNSRecordID, ExactName: record.Name, Owner: a.Owner, Fingerprint: trafficFingerprint(record)}, nil
}

func (d *Adapter) Seal(ctx context.Context, a cdn.Attachment, step cdn.PlannedStep, prior cdn.OwnedReceipt) (cdn.SealedAction, error) {
	if err := d.admit(a); err != nil {
		return cdn.SealedAction{}, err
	}
	if step.Profile != d.scope {
		return cdn.SealedAction{}, cdn.ErrInvalid
	}
	observed, err := d.Observe(ctx, a)
	if err != nil {
		return cdn.SealedAction{}, err
	}
	current := selected(observed, step.ResourceKind)
	if len(current.Resources) != 1 {
		return cdn.SealedAction{}, cdn.ErrInvalid
	}
	desired, err := d.stepDesired(a, step)
	if err != nil {
		return cdn.SealedAction{}, err
	}
	if desired.ExactName != step.ExactName || desired.Fingerprint != step.Desired {
		return cdn.SealedAction{}, cdn.ErrConflict
	}
	if err = d.readyForStep(a, step, observed); err != nil {
		return cdn.SealedAction{}, err
	}
	return cdn.SealedAction{Action: cdn.Action{Kind: step.Kind, Attachment: a, Profile: d.scope, Resource: current.Resources[0], Expected: current.Fingerprint, Desired: desired.Fingerprint}, Current: current}, nil
}

func (d *Adapter) stepDesired(a cdn.Attachment, step cdn.PlannedStep) (cdn.OwnedResource, error) {
	if d.traffic {
		if step.Kind != cdn.SetTraffic || step.ResourceKind != cdn.HostnameTraffic {
			return cdn.OwnedResource{}, cdn.ErrInvalid
		}
		return d.desiredTraffic(a)
	}
	if !matchesAction(step.Kind, step.ResourceKind) {
		return cdn.OwnedResource{}, cdn.ErrInvalid
	}
	if a.Mode == cdn.Enabled && (step.Kind == cdn.RemoveEdge || step.Kind == cdn.RemoveEdgeRoute) || a.Mode == cdn.Direct && (step.Kind == cdn.PutEdge || step.Kind == cdn.PutEdgeRoute) {
		return cdn.OwnedResource{}, cdn.ErrInvalid
	}
	return d.desiredResource(a, step.ResourceKind)
}

func (d *Adapter) readyForStep(a cdn.Attachment, step cdn.PlannedStep, observed cdn.Observation) error {
	if d.traffic || step.Kind != cdn.SetEdgeExposure && step.Kind != cdn.PutEdgeRoute {
		return nil
	}
	wanted, err := d.desiredResource(a, cdn.EdgeConfiguration)
	if err != nil {
		return err
	}
	if selected(observed, cdn.EdgeConfiguration).Fingerprint != wanted.Fingerprint {
		return cdn.ErrConflict
	}
	if step.Kind != cdn.PutEdgeRoute {
		return nil
	}
	wanted, err = d.desiredResource(a, cdn.EdgeExposure)
	if err != nil {
		return err
	}
	if selected(observed, cdn.EdgeExposure).Fingerprint != wanted.Fingerprint {
		return cdn.ErrConflict
	}
	return nil
}
