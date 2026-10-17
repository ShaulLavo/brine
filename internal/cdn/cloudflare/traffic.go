package cloudflare

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type dnsRecord struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Proxied    bool   `json:"proxied"`
	TTL        int    `json:"ttl"`
	ModifiedOn string `json:"modified_on"`
}

type trafficConfiguration struct {
	Type    string
	Name    string
	Content string
	Proxied bool
}

func (r *dnsRecord) UnmarshalJSON(raw []byte) error {
	var fields struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Name       string `json:"name"`
		Content    string `json:"content"`
		Proxied    *bool  `json:"proxied"`
		TTL        int    `json:"ttl"`
		ModifiedOn string `json:"modified_on"`
	}
	if json.Unmarshal(raw, &fields) != nil || fields.Proxied == nil {
		return cdn.ErrInvalid
	}
	*r = dnsRecord{fields.ID, fields.Type, fields.Name, fields.Content, *fields.Proxied, fields.TTL, fields.ModifiedOn}
	return nil
}

func trafficFingerprint(record dnsRecord) cdn.Fingerprint {
	return fingerprint(trafficConfiguration{record.Type, record.Name, record.Content, record.Proxied})
}

func (d *Adapter) readTraffic(ctx context.Context, a cdn.Attachment) (dnsRecord, error) {
	var record dnsRecord
	if err := d.client.get(ctx, d.dnsPath(), ReadDNS, &record); err != nil {
		return record, err
	}
	origin, err := url.Parse(a.Binding.Origin.Address)
	if err != nil || record.ID != d.client.profile.document.DNSRecordID || record.Name != a.Binding.Hostname || record.TTL < 1 {
		return record, &APIError{Kind: OwnershipConflict, Permission: ReadDNS}
	}
	if record.Type != "A" && record.Type != "AAAA" && record.Type != "CNAME" {
		return record, &APIError{Kind: OwnershipConflict, Permission: ReadDNS}
	}
	if strings.TrimSuffix(record.Content, ".") != origin.Hostname() {
		return record, &APIError{Kind: OwnershipConflict, Permission: ReadDNS}
	}
	return record, nil
}

func (d *Adapter) observeTraffic(ctx context.Context, a cdn.Attachment) (cdn.Observation, error) {
	record, err := d.readTraffic(ctx, a)
	if err != nil {
		return cdn.Observation{}, err
	}
	resource := cdn.OwnedResource{Kind: cdn.HostnameTraffic, ID: record.ID, ExactName: record.Name,
		Owner: a.Owner, Version: record.ModifiedOn, Fingerprint: trafficFingerprint(record)}
	return observation(d.scope, []cdn.OwnedResource{resource}), nil
}
