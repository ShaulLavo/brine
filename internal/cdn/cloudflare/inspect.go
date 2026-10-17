package cloudflare

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/cdn"
)

func desiredSettings(a cdn.Attachment, source []byte) scriptSettings {
	settings := scriptSettings{Tags: []string{ownerTag(a), "brine-cdn-source:" + string(cdn.Hash(source))},
		Bindings: make([]json.RawMessage, 0), CompatibilityDate: compatibilityDate, CompatibilityFlags: make([]string, 0)}
	settings.Observability.Enabled = true
	settings.Observability.RedactQueryString = true
	settings.Observability.Logs.Enabled = true
	settings.Observability.Traces.Enabled = true
	slices.Sort(settings.Tags)
	return settings
}

func (d *Adapter) desiredResource(a cdn.Attachment, kind cdn.ResourceKind) (cdn.OwnedResource, error) {
	name := workerName(d.client.profile, a)
	resource := cdn.OwnedResource{Kind: kind, ID: name, ExactName: name, Owner: a.Owner}
	switch kind {
	case cdn.EdgeConfiguration:
		if a.Mode == cdn.Direct {
			return missing(kind, name, a), nil
		}
		source, err := WorkerSource(a)
		if err != nil {
			return cdn.OwnedResource{}, err
		}
		resource.Fingerprint = fingerprint(scriptConfiguration{cdn.Hash(source), desiredSettings(a, source)})
	case cdn.EdgeExposure:
		resource.Fingerprint = fingerprint(exposure{})
	case cdn.EdgeRoute:
		pattern := d.client.profile.document.RoutePattern
		if a.Mode == cdn.Direct {
			return missing(kind, pattern, a), nil
		}
		resource.ExactName, resource.ID = pattern, ""
		resource.Fingerprint = fingerprint(struct{ Pattern, Script string }{pattern, name})
	default:
		return cdn.OwnedResource{}, cdn.ErrInvalid
	}
	return resource, nil
}

func (d *Adapter) Inspect(ctx context.Context, permit cdn.Permit) (cdn.Inspection, error) {
	if err := d.admitPermit(permit); err != nil {
		return nil, err
	}
	current, err := d.Observe(ctx, permit.Action.Attachment)
	if err != nil {
		return nil, err
	}
	current = selected(current, permit.Action.Resource.Kind)
	relation := cdn.Diverged
	if current.Fingerprint == permit.Action.Expected {
		relation = cdn.Before
	}
	if current.Fingerprint == permit.Action.Desired {
		relation = cdn.Desired
	}
	if len(current.Resources) == 1 && current.Resources[0].ID == "" && relation == cdn.Diverged {
		relation = cdn.Absent
	}
	return cdn.Observed{Effect: permit.Effect, Current: current, Relation: relation}, nil
}

func (d *Adapter) admitPermit(p cdn.Permit) error {
	if p.Operation == "" || p.Effect == "" || p.Claim == "" || p.Action.Profile != d.scope || p.Action.Expected == p.Action.Desired {
		return cdn.ErrInvalid
	}
	if err := d.admit(p.Action.Attachment); err != nil {
		return err
	}
	if p.Action.Resource.Owner != p.Action.Attachment.Owner {
		return &APIError{Kind: OwnershipConflict}
	}
	kind := p.Action.Resource.Kind
	if d.traffic {
		if p.Action.Kind != cdn.SetTraffic || kind != cdn.HostnameTraffic || p.Action.Resource.ID != d.client.profile.document.DNSRecordID || p.Action.Resource.ExactName != p.Action.Attachment.Binding.Hostname {
			return cdn.ErrInvalid
		}
		return nil
	}
	if p.Action.Attachment.Mode == cdn.Direct && (p.Action.Kind == cdn.PutEdge || p.Action.Kind == cdn.PutEdgeRoute) {
		return cdn.ErrInvalid
	}
	if p.Action.Attachment.Mode == cdn.Enabled && (p.Action.Kind == cdn.RemoveEdge || p.Action.Kind == cdn.RemoveEdgeRoute) {
		return cdn.ErrInvalid
	}
	if (p.Action.Kind == cdn.RemoveEdge || p.Action.Kind == cdn.RemoveEdgeRoute) && p.Action.Resource.ID == "" {
		return cdn.ErrInvalid
	}
	if p.Action.Kind == cdn.PutEdge && p.Action.Resource.ID != "" {
		return cdn.ErrInvalid
	}
	if !matchesAction(p.Action.Kind, kind) {
		return cdn.ErrInvalid
	}
	resource, err := d.desiredResource(p.Action.Attachment, kind)
	if err != nil || resource.ExactName != p.Action.Resource.ExactName || resource.Fingerprint != p.Action.Desired {
		return cdn.ErrInvalid
	}
	if kind == cdn.EdgeRoute && p.Action.Resource.ID != "" && !nativeID.MatchString(p.Action.Resource.ID) {
		return cdn.ErrInvalid
	}
	if kind != cdn.EdgeRoute && p.Action.Resource.ID != "" && p.Action.Resource.ID != resource.ExactName {
		return cdn.ErrInvalid
	}
	return nil
}

func matchesAction(action cdn.ActionKind, resource cdn.ResourceKind) bool {
	switch resource {
	case cdn.EdgeConfiguration:
		return action == cdn.PutEdge || action == cdn.RemoveEdge
	case cdn.EdgeExposure:
		return action == cdn.SetEdgeExposure
	case cdn.EdgeRoute:
		return action == cdn.PutEdgeRoute || action == cdn.RemoveEdgeRoute
	default:
		return false
	}
}
