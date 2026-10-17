package policy

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
)

func TestCDNPermissionGrantReviewAndExactRevocation(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	binding := cdnBindingFixture("public")
	granted, encoded, err := p.EditPermissions([]PermissionAction{{Kind: AllowCDNBinding, CDNBinding: &binding}})
	if err != nil {
		t.Fatal(err)
	}
	assertOtherPolicyFieldsEqual(t, p, granted, "cdn_bindings")
	if _, ok := p.CDNBinding("public", "hello"); ok {
		t.Fatal("grant mutated original policy")
	}
	parsed, err := Parse(encoded)
	if err != nil || parsed.Hash() != granted.Hash() {
		t.Fatal("CDN permission grant did not survive TOML round trip", err)
	}
	view := granted.Permissions()
	if len(view.CDNBindings) != 1 || view.CDNBindings[0].Reference != "public" {
		t.Fatal("review view omitted typed CDN authority")
	}
	view.CDNBindings[0].Cache.Extensions[0] = "exe"
	view.CDNBindings[0].AllowedEdges[0].Fingerprint = cdn.Hash([]byte("other"))
	lookedUp, ok := granted.CDNBinding("public", "hello")
	want, _ := binding.CanonicalBytes()
	got, _ := lookedUp.CanonicalBytes()
	if !ok || !bytes.Equal(want, got) {
		t.Fatal("permission view mutated CDN policy")
	}
	idempotent, _, err := granted.EditPermissions([]PermissionAction{{Kind: AllowCDNBinding, CDNBinding: &binding}})
	if err != nil || idempotent.Hash() != granted.Hash() {
		t.Fatal("repeated exact grant was not a no-op", err)
	}
	revoked, _, err := granted.EditPermissions([]PermissionAction{{Kind: RevokeCDNBinding, CDNBinding: &binding}})
	if err != nil || revoked.Hash() != p.Hash() {
		t.Fatal("exact CDN revocation failed", err)
	}
	absent, _, err := revoked.EditPermissions([]PermissionAction{{Kind: RevokeCDNBinding, CDNBinding: &binding}})
	if err != nil || absent.Hash() != revoked.Hash() {
		t.Fatal("absent exact revocation was not a no-op", err)
	}
}

func TestOtherPermissionEditsPreserveCDNBinding(t *testing.T) {
	binding := cdnBindingFixture("public")
	p, err := Parse(policyWithCDN(t, binding))
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := p.EditPermissions([]PermissionAction{{Kind: AllowDomain, Domain: "other.example.test"}, {Kind: AllowRegistry, Registry: &Registry{Host: "registry.example", RepositoryPrefixes: []string{"demo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	assertOtherPolicyFieldsEqual(t, p, after, "allowed_registries", "allowed_domains")
	binding.Cache.Prefix = "/new-objects/"
	replaced, _, err := after.EditPermissions([]PermissionAction{{Kind: AllowCDNBinding, CDNBinding: &binding}})
	if err != nil {
		t.Fatal("reviewed replacement grant failed", err)
	}
	if len(replaced.Permissions().CDNBindings) != 1 {
		t.Fatal("replacement duplicated CDN reference")
	}
	if _, _, err := p.EditPermissions([]PermissionAction{{Kind: RevokeCDNBinding, CDNBinding: &binding}}); err == nil {
		t.Fatal("revocation accepted a different namespace for the same reference")
	}
}

func TestCDNPermissionRefusesMixedAndRepeatedAuthorities(t *testing.T) {
	binding := cdnBindingFixture("public")
	p, err := Parse(policyWithCDN(t, binding))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := p.CanonicalBytes()
	invalid := cdnBindingFixture("invalid")
	invalid.Origin.Address = "http://192.0.2.5"
	for _, actions := range [][]PermissionAction{
		{{Kind: AllowCDNBinding}},
		{{Kind: RevokeCDNBinding}},
		{{Kind: AllowCDNBinding, CDNBinding: &invalid}},
		{{Kind: AllowCDNBinding, CDNBinding: &binding, Domain: "other.example.test"}},
		{{Kind: AllowCDNBinding, CDNBinding: &binding, Registry: &Registry{Host: "registry.example", RepositoryPrefixes: []string{"demo"}}}},
		{{Kind: AllowDomain, Domain: "other.example.test", CDNBinding: &binding}},
		{{Kind: AllowRegistry, Registry: &Registry{Host: "registry.example", RepositoryPrefixes: []string{"demo"}}, CDNBinding: &binding}},
		{{Kind: RevokeCDNBinding, CDNBinding: &binding}, {Kind: AllowCDNBinding, CDNBinding: &binding}},
	} {
		if _, _, err := p.EditPermissions(actions); err == nil {
			t.Fatal("invalid or repeated CDN authority accepted")
		}
	}
	after, _ := p.CanonicalBytes()
	if !bytes.Equal(before, after) {
		t.Fatal("rejected CDN edit mutated original policy")
	}
}

func assertOtherPolicyFieldsEqual(t *testing.T, before, after Policy, changed ...string) {
	t.Helper()
	beforeJSON, _ := before.CanonicalBytes()
	afterJSON, _ := after.CanonicalBytes()
	var beforeFields, afterFields map[string]any
	if json.Unmarshal(beforeJSON, &beforeFields) != nil || json.Unmarshal(afterJSON, &afterFields) != nil {
		t.Fatal("could not decode canonical policy")
	}
	for _, key := range changed {
		delete(beforeFields, key)
		delete(afterFields, key)
	}
	if !reflect.DeepEqual(beforeFields, afterFields) {
		t.Fatal("permission edit changed unrelated policy fields")
	}
}
