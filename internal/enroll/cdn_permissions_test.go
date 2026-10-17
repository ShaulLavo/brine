package enroll

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cdn"
	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
)

func permissionCDNFixture() cdn.Binding {
	profile := cdn.ProfileScope{Reference: "fixture", Driver: "local-http", Fingerprint: cdn.Hash([]byte("fixture-profile"))}
	return cdn.Binding{Reference: "public", App: "demo", Target: cdn.Hash([]byte("fixture-target")), Hostname: "demo.example.test",
		Origin:       cdn.Origin{Address: "https://192.0.2.5", Host: "demo.example.test", TLSName: "demo.example.test"},
		Cache:        cdn.CachePolicy{Prefix: "/objects/", Extensions: []string{"bin"}, TTLSeconds: cdn.ImmutableTTL},
		AllowedEdges: []cdn.ProfileScope{profile}, Traffic: profile,
		TrafficAuthority: cdn.TrafficAuthority{ResourceID: "fixture-record", Hostname: "demo.example.test", Before: cdn.Hash([]byte("fixture-before"))}}
}

func TestCDNPermissionPlanApplyReadbackAndRevoke(t *testing.T) {
	s, memory, _ := permissionFixture(t)
	binding := permissionCDNFixture()
	request := PermissionsRequest{Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowCDNBinding, CDNBinding: &binding}}}
	response, err := s.run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	plan := response.Plan
	if len(plan.Before.CDNBindings) != 0 || len(plan.After.CDNBindings) != 1 || plan.NoOp {
		t.Fatal("plan did not expose the exact before/after CDN authority")
	}
	encoded, err := json.Marshal(result.Success("brine host permissions", response))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePermissionsResponse(encoded, request)
	if err != nil || decoded.Plan.After.CDNBindings[0].TrafficAuthority != binding.TrafficAuthority {
		t.Fatal("typed CDN permission plan did not cross the machine boundary", err)
	}
	apply := permissionApply(*plan, "a")
	if _, err := s.run(t.Context(), apply); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(t.Context(), apply); err != nil || memory.writes != 1 {
		t.Fatal("duplicate CDN grant replayed policy mutation", err, memory.writes)
	}
	parsed, err := operatorpolicy.Parse(memory.raw)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := parsed.CDNBinding("public", "demo")
	want, _ := binding.CanonicalBytes()
	got, _ := actual.CanonicalBytes()
	if !ok || !bytes.Equal(want, got) {
		t.Fatal("installed policy did not retain exact CDN binding")
	}
	request.Changes[0].Kind = operatorpolicy.RevokeCDNBinding
	revocation, err := s.run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(t.Context(), permissionApply(*revocation.Plan, "b")); err != nil || memory.writes != 2 {
		t.Fatal("CDN revocation failed", err, memory.writes)
	}
	shown, err := s.run(t.Context(), PermissionsRequest{Action: "show"})
	if err != nil || len(shown.Permissions.CDNBindings) != 0 {
		t.Fatal("revoked CDN binding remained in policy view", err)
	}
}

func TestCDNPermissionWireRejectsOpenAndMixedVariants(t *testing.T) {
	binding := permissionCDNFixture()
	request := PermissionsRequest{Action: "plan", IdentityKey: make([]byte, 32), HostKey: "fixture-host", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowCDNBinding, CDNBinding: &binding}}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePermissionsRequest(raw); err != nil {
		t.Fatal("valid CDN grant rejected", err)
	}
	request.Changes[0].Kind = operatorpolicy.RevokeCDNBinding
	checkPermissionRequestRoundTrip(t, request)
	valid := string(raw)
	for _, test := range []struct{ from, to string }{
		{`"cdn_binding":{`, `"cdn_binding":null,"extra":{`},
		{`"cdn_binding":{`, `"CDN_BINDING":{`},
		{`"kind":"allow_cdn_binding"`, `"kind":"allow_cdn_binding","domain":"demo.example.test"`},
		{`"kind":"allow_cdn_binding"`, `"kind":"allow_cdn_binding","registry":null`},
		{`"reference":"public"`, `"reference":"public","reference":"public"`},
		{`"tls_name":`, `"TLSName":`},
		{`"ttl_seconds":`, `"TTLSeconds":`},
		{`"allowed_edges":[{`, `"allowed_edges":[{"credential":"do-not-echo",`},
		{`"traffic":{`, `"traffic":{"credential":"do-not-echo",`},
		{`"resource_id":`, `"ResourceID":`},
	} {
		changed := strings.Replace(valid, test.from, test.to, 1)
		if changed == valid {
			t.Fatal("test did not change request")
		}
		if _, err := DecodePermissionsRequest([]byte(changed)); err == nil {
			t.Fatal("open or mixed CDN permission variant accepted")
		}
	}
}

func TestCDNPermissionViewWireClosesBindingObjects(t *testing.T) {
	s, _, _ := permissionFixture(t)
	binding := permissionCDNFixture()
	response, err := s.run(t.Context(), PermissionsRequest{Action: "plan", Changes: []operatorpolicy.PermissionAction{{Kind: operatorpolicy.AllowCDNBinding, CDNBinding: &binding}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.Success("brine host permissions", response))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	for _, test := range []struct{ from, to string }{
		{`"cdn_bindings":[]`, `"cdn_bindings":null`},
		{`"cdn_bindings":[]`, `"CDNBindings":[]`},
		{`"cdn_bindings":[{`, `"cdn_bindings":[{"reference":"public",`},
		{`"cdn_bindings":[{`, `"cdn_bindings":[{"credential":"do-not-echo",`},
	} {
		changed := strings.Replace(valid, test.from, test.to, 1)
		if changed == valid {
			t.Fatal("test did not change response")
		}
		if _, err := decodePermissionsResponse([]byte(changed), PermissionsRequest{Action: "plan"}); err == nil {
			t.Fatal("open CDN binding view accepted")
		}
	}
}
