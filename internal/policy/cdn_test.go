package policy

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/pelletier/go-toml/v2"
)

func cdnBindingFixture(reference string) cdn.Binding {
	return cdn.Binding{
		Reference: reference, App: "hello", Target: cdn.Hash([]byte("fixture-target")), Hostname: "web.example.com",
		Origin: cdn.Origin{Address: "https://192.0.2.5", Host: "web.example.com", TLSName: "web.example.com"},
		Cache:  cdn.CachePolicy{Prefix: "/objects/", Extensions: []string{"png", "bin"}, TTLSeconds: cdn.ImmutableTTL},
		AllowedEdges: []cdn.ProfileScope{
			{Reference: "edge-b", Driver: "fixture", Fingerprint: cdn.Hash([]byte("fixture-b"))},
			{Reference: "edge-a", Driver: "fixture", Fingerprint: cdn.Hash([]byte("fixture-a"))},
		},
		Traffic:          cdn.ProfileScope{Reference: "traffic", Driver: "fixture", Fingerprint: cdn.Hash([]byte("fixture-traffic"))},
		TrafficAuthority: cdn.TrafficAuthority{ResourceID: "fixture-hostname", Hostname: "web.example.com", Before: cdn.Hash([]byte("fixture-before"))},
	}
}

func policyWithCDN(t *testing.T, bindings ...cdn.Binding) []byte {
	t.Helper()
	var raw document
	if err := toml.Unmarshal(fixture(t), &raw); err != nil {
		t.Fatal(err)
	}
	raw.CDNBindings = bindings
	encoded, err := toml.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestCDNBindingsCanonicalPolicy(t *testing.T) {
	a, b := cdnBindingFixture("public-a"), cdnBindingFixture("public-b")
	input := policyWithCDN(t, b, a)
	first, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(a.Cache.Extensions)
	slices.Reverse(a.AllowedEdges)
	second, err := Parse(policyWithCDN(t, a, b))
	if err != nil || first.Hash() != second.Hash() {
		t.Fatal("CDN binding order changed canonical policy", err)
	}
	if first.config.CDNBindings[0].Reference != "public-a" {
		t.Fatal("CDN binding references are not sorted")
	}
	got, ok := first.CDNBinding("public-a", "hello")
	want, err := a.CanonicalBytes()
	encoded, gotErr := got.CanonicalBytes()
	if !ok || err != nil || gotErr != nil || !bytes.Equal(want, encoded) {
		t.Fatal("CDN lookup did not return the exact canonical binding")
	}
	if got.Cache.Extensions[0] != "bin" || got.AllowedEdges[0].Reference != "edge-a" {
		t.Fatal("nested CDN policy is not canonically sorted")
	}
	roundTrip, err := toml.Marshal(first.config)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(roundTrip)
	if err != nil || parsed.Hash() != first.Hash() {
		t.Fatal("TOML round trip changed CDN policy", err)
	}
	if _, err := Normalize(app(t), parsed); err != nil {
		t.Fatal("CDN policy changed unrelated application permissions", err)
	}
	b.Traffic.Fingerprint = cdn.Hash([]byte("changed-traffic-scope"))
	changed, err := Parse(policyWithCDN(t, a, b))
	if err != nil || changed.Hash() == first.Hash() {
		t.Fatal("CDN authority change did not change policy digest", err)
	}
}

func TestCDNBindingLookupIsIndependent(t *testing.T) {
	p, err := Parse(policyWithCDN(t, cdnBindingFixture("public")))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := p.CanonicalBytes()
	got, ok := p.CDNBinding("public", "hello")
	if !ok {
		t.Fatal("expected authorized CDN binding")
	}
	got.Cache.Extensions[0] = "exe"
	got.AllowedEdges[0].Fingerprint = cdn.Hash([]byte("foreign-scope"))
	got.Hostname = "foreign.example.test"
	after, _ := p.CanonicalBytes()
	if !bytes.Equal(before, after) {
		t.Fatal("returned CDN binding mutated private policy")
	}
	for _, lookup := range [][2]string{{"public", "other"}, {"missing", "hello"}, {"", "hello"}} {
		if _, ok := p.CDNBinding(lookup[0], lookup[1]); ok {
			t.Fatal("CDN lookup authorized another app or reference")
		}
	}
	if _, ok := (Policy{}).CDNBinding("public", "hello"); ok {
		t.Fatal("zero policy authorized CDN binding")
	}
}

func TestCDNBindingAbsentPreservesPolicyDigest(t *testing.T) {
	omitted, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Parse(append([]byte("cdn_bindings = []\n"), fixture(t)...))
	canonical, canonicalErr := explicit.CanonicalBytes()
	if err != nil || canonicalErr != nil || omitted.Hash() != explicit.Hash() || bytes.Contains(canonical, []byte("cdn_bindings")) {
		t.Fatal("empty optional CDN bindings changed existing policy", err, canonicalErr)
	}
}

func TestCDNBindingRefusesConflictingIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*cdn.Binding)
	}{
		{"duplicate reference", func(b *cdn.Binding) { b.Reference = "public" }},
		{"another app hostname", func(b *cdn.Binding) { b.App = "other" }},
		{"another target hostname", func(b *cdn.Binding) { b.Target = cdn.Hash([]byte("another-target")) }},
		{"invalid app", func(b *cdn.Binding) { b.App = "Invalid" }},
		{"invalid app underscore", func(b *cdn.Binding) { b.App = "bad_app" }},
		{"invalid app final hyphen", func(b *cdn.Binding) { b.App = "bad-" }},
		{"invalid reference", func(b *cdn.Binding) { b.Reference = "bad/reference" }},
		{"wildcard hostname", func(b *cdn.Binding) { b.Hostname = "*.example.com" }},
		{"crossed authority hostname", func(b *cdn.Binding) { b.TrafficAuthority.Hostname = "other.example.com" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			other := cdnBindingFixture("alternate")
			test.change(&other)
			_, err := Parse(policyWithCDN(t, cdnBindingFixture("public"), other))
			assertCDNPolicyRefusal(t, err, "policy.invalid_cdn_binding")
		})
	}
}

func TestCDNBindingUsesAppNameGrammar(t *testing.T) {
	binding := cdnBindingFixture("public")
	binding.App = "1"
	p, err := Parse(policyWithCDN(t, binding))
	if err != nil {
		t.Fatal("valid numeric app name refused", err)
	}
	if _, ok := p.CDNBinding("public", "1"); !ok {
		t.Fatal("numeric app name lookup failed")
	}
}

func TestCDNBindingStrictNestedTOML(t *testing.T) {
	valid := policyWithCDN(t, cdnBindingFixture("public"))
	for _, test := range []struct{ name, from, to string }{
		{"root alias", "[[cdn_bindings]]", "[[CDNBindings]]"},
		{"binding alias", "reference = 'public'", "Reference = 'public'"},
		{"binding unknown", "app = 'hello'", "secret = 'do-not-echo'\napp = 'hello'"},
		{"origin alias", "tls_name =", "TLSName ="},
		{"origin unknown", "address =", "secret = 'do-not-echo'\naddress ="},
		{"cache alias", "ttl_seconds =", "TTLSeconds ="},
		{"cache unknown", "prefix =", "secret = 'do-not-echo'\nprefix ="},
		{"edge alias", "driver = 'fixture'", "Driver = 'fixture'"},
		{"edge table shape", "[[cdn_bindings.allowed_edges]]", "[cdn_bindings.allowed_edges]"},
		{"traffic alias", "[cdn_bindings.traffic]", "[cdn_bindings.Traffic]"},
		{"traffic unknown", "reference = 'traffic'", "secret = 'do-not-echo'\nreference = 'traffic'"},
		{"authority alias", "resource_id =", "ResourceID ="},
		{"authority unknown", "resource_id =", "secret = 'do-not-echo'\nresource_id ="},
		{"binding table shape", "[[cdn_bindings]]", "[cdn_bindings]"},
	} {
		t.Run(test.name, func(t *testing.T) { checkCDNPolicyMutation(t, valid, test.from, test.to) })
	}
}

func checkCDNPolicyMutation(t *testing.T, valid []byte, from, to string) {
	t.Helper()
	mutated := bytes.Replace(valid, []byte(from), []byte(to), 1)
	if bytes.Equal(mutated, valid) {
		t.Fatal("test did not change TOML")
	}
	_, err := Parse(mutated)
	assertCDNPolicyRefusal(t, err, "policy.invalid_document")
}

func assertCDNPolicyRefusal(t *testing.T, err error, code string) {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("expected fixed CDN policy refusal %s, got %v", code, err)
	}
	if strings.Contains(err.Error(), "do-not-echo") {
		t.Fatal("CDN policy refusal leaked input")
	}
}
