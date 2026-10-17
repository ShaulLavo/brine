// Package cloudflare implements the Cloudflare CDN adapter.
package cloudflare

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type Adapter struct {
	client  nativeClient
	scope   cdn.ProfileScope
	traffic bool
}

func NewEdge(profile Profile, reference cdn.ProfileRef, tokens TokenSource) (*Adapter, error) {
	return newAdapter(profile, reference, tokens, false)
}

func NewTraffic(profile Profile, reference cdn.ProfileRef, tokens TokenSource) (*Adapter, error) {
	return newAdapter(profile, reference, tokens, true)
}

func newAdapter(profile Profile, reference cdn.ProfileRef, tokens TokenSource, traffic bool) (*Adapter, error) {
	scope, err := profile.Scope(reference)
	if err != nil || tokens == nil {
		return nil, ErrProfile
	}
	return &Adapter{client: newClient(profile, tokens), scope: scope, traffic: traffic}, nil
}

func (d *Adapter) admit(a cdn.Attachment) error {
	if a.Binding.Validate() != nil || a.Owner.App != a.Binding.App || a.Owner.Target != a.Binding.Target || a.Owner.Attachment == "" || a.Owner.Incarnation == "" || a.Revision == "" {
		return cdn.ErrInvalid
	}
	if a.Mode != cdn.Enabled && a.Mode != cdn.Direct {
		return cdn.ErrInvalid
	}
	p := d.client.profile.document
	if p.RoutePattern != "https://"+a.Binding.Hostname+a.Binding.Cache.Prefix+"*" || p.DNSRecordID != a.Binding.TrafficAuthority.ResourceID {
		return ErrProfile
	}
	if a.Binding.Origin.Host != a.Binding.Hostname || a.Binding.Origin.TLSName != a.Binding.Hostname {
		return ErrProfile
	}
	origin, err := url.Parse(a.Binding.Origin.Address)
	if err != nil || origin.Port() != "" && origin.Port() != "443" {
		return ErrProfile
	}
	if !slices.Contains(a.Binding.AllowedEdges, a.Profile) {
		return ErrProfile
	}
	if d.traffic && a.Binding.Traffic != d.scope || !d.traffic && a.Profile != d.scope {
		return ErrProfile
	}
	return nil
}

func (d *Adapter) Observe(ctx context.Context, a cdn.Attachment) (cdn.Observation, error) {
	if err := d.admit(a); err != nil {
		return cdn.Observation{}, err
	}
	if d.traffic {
		return d.observeTraffic(ctx, a)
	}
	return d.observeEdge(ctx, a)
}

func workerName(profile Profile, a cdn.Attachment) string {
	raw, _ := json.Marshal(struct {
		Owner    cdn.Owner
		Revision cdn.RevisionID
	}{a.Owner, a.Revision})
	return profile.document.WorkerPrefix + "-" + strings.TrimPrefix(string(cdn.Hash(raw)), "sha256:")[:24]
}

func ownerTag(a cdn.Attachment) string {
	raw, _ := json.Marshal(a.Owner)
	return "brine-cdn-owner:" + strings.TrimPrefix(string(cdn.Hash(raw)), "sha256:")
}

func fingerprint(value any) cdn.Fingerprint {
	raw, _ := json.Marshal(value)
	return cdn.Hash(raw)
}

func missing(kind cdn.ResourceKind, name string, a cdn.Attachment) cdn.OwnedResource {
	return cdn.OwnedResource{Kind: kind, ExactName: name, Owner: a.Owner,
		Fingerprint: fingerprint(struct {
			Kind   cdn.ResourceKind
			Name   string
			Exists bool
		}{kind, name, false})}
}

func observation(scope cdn.ProfileScope, resources []cdn.OwnedResource) cdn.Observation {
	return cdn.Observation{Profile: scope, Resources: resources, Fingerprint: fingerprint(resources), ObservedAt: time.Now().UTC()}
}

func selected(observed cdn.Observation, kind cdn.ResourceKind) cdn.Observation {
	for _, resource := range observed.Resources {
		if resource.Kind != kind {
			continue
		}
		observed.Resources = []cdn.OwnedResource{resource}
		observed.Fingerprint = resource.Fingerprint
		return observed
	}
	return cdn.Observation{}
}

func (d *Adapter) scriptPath(name string) string {
	return nativePath("accounts", d.client.profile.document.AccountID, "workers", "scripts", url.PathEscape(name))
}

func (d *Adapter) routePath() string {
	return nativePath("zones", d.client.profile.document.ZoneID, "workers", "routes")
}

func (d *Adapter) dnsPath() string {
	return nativePath("zones", d.client.profile.document.ZoneID, "dns_records", d.client.profile.document.DNSRecordID)
}
