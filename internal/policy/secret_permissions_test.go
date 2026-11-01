package policy

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func secretPermissionAction(kind PermissionKind, app, reference string) PermissionAction {
	return PermissionAction{Kind: kind, Secret: &SecretPermission{App: spec.Name(app), Reference: spec.SecretReference(reference)}}
}

func TestSecretPermissionsExactGrantRevokeAndPreservation(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := p.CanonicalBytes()
	grant := secretPermissionAction("allow_secret", "waffle-relay", "relay-signing")
	after, encoded, err := p.EditPermissions([]PermissionAction{grant, secretPermissionAction("allow_secret", "other", "relay-signing"), secretPermissionAction("allow_secret", "waffle-relay", "other-ref")})
	if err != nil {
		t.Fatal("exact secret grant refused", err)
	}
	assertOtherPolicyFieldsEqual(t, p, after, "allowed_secrets")
	unchanged, _ := p.CanonicalBytes()
	if !bytes.Equal(before, unchanged) || p.CheckSecret("waffle-relay", "relay-signing") == nil {
		t.Fatal("secret edit mutated the input policy")
	}
	roundTrip, err := Parse(encoded)
	if err != nil || roundTrip.Hash() != after.Hash() || roundTrip.CheckSecret("waffle-relay", "relay-signing") != nil {
		t.Fatal("grant did not survive policy round trip", err)
	}
	view := after.Permissions()
	view.AllowedSecrets["waffle-relay"][0] = "changed"
	delete(view.AllowedSecrets, "other")
	view.AllowedSecrets["injected"] = []string{"ref"}
	if after.CheckSecret("waffle-relay", "other-ref") != nil || after.CheckSecret("other", "relay-signing") != nil || after.CheckSecret("injected", "ref") == nil {
		t.Fatal("permission view aliases policy maps or slices")
	}
	noOp, _, err := after.EditPermissions([]PermissionAction{grant})
	if err != nil || noOp.Hash() != after.Hash() {
		t.Fatal("existing grant was not a no-op", err)
	}
	revoked, _, err := after.EditPermissions([]PermissionAction{secretPermissionAction("revoke_secret", "waffle-relay", "relay-signing")})
	if err != nil || revoked.CheckSecret("waffle-relay", "relay-signing") == nil {
		t.Fatal("exact revocation failed", err)
	}
	if revoked.CheckSecret("other", "relay-signing") != nil || revoked.CheckSecret("waffle-relay", "other-ref") != nil {
		t.Fatal("revocation changed another app or reference")
	}
	if after.CheckSecret("waffle-relay", "relay-signing") != nil {
		t.Fatal("revocation mutated its input policy")
	}
	absent, _, err := revoked.EditPermissions([]PermissionAction{secretPermissionAction("revoke_secret", "waffle-relay", "relay-signing")})
	if err != nil || absent.Hash() != revoked.Hash() {
		t.Fatal("absent revocation was not a no-op", err)
	}
}

func TestSecretPermissionsRejectMalformedRepeatedAndMixedActions(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := p.CanonicalBytes()
	for _, pair := range [][2]string{{"", "ref"}, {"*", "ref"}, {"../app", "ref"}, {"App", "ref"}, {"app/name", "ref"}, {"app", ""}, {"app", "*"}, {"app", "../ref"}, {"app", "ref/name"}, {"app", "ref.value"}, {"app", strings.Repeat("r", 253)}} {
		action := secretPermissionAction("allow_secret", pair[0], pair[1])
		if _, _, err := p.EditPermissions([]PermissionAction{action}); err == nil {
			t.Fatal("malformed secret scope accepted", pair)
		}
	}
	grant := secretPermissionAction("allow_secret", "app", "ref")
	revoke := secretPermissionAction("revoke_secret", "app", "ref")
	mixed := grant
	mixed.Domain = "demo.example"
	for _, actions := range [][]PermissionAction{{grant, grant}, {grant, revoke}, {revoke, grant}, {mixed}} {
		if _, _, err := p.EditPermissions(actions); err == nil {
			t.Fatal("repeated, contradictory or mixed secret action accepted")
		}
	}
	after, _ := p.CanonicalBytes()
	if !bytes.Equal(before, after) {
		t.Fatal("rejected edit mutated policy")
	}
}

func TestSecretPermissionsCaseSensitiveExactReferences(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := p.EditPermissions([]PermissionAction{secretPermissionAction("allow_secret", "app", "Ref"), secretPermissionAction("allow_secret", "app", "ref")})
	if err != nil {
		t.Fatal(err)
	}
	revoked, _, err := after.EditPermissions([]PermissionAction{secretPermissionAction("revoke_secret", "app", "Ref")})
	if err != nil || revoked.CheckSecret("app", "ref") != nil || revoked.CheckSecret("app", "Ref") == nil {
		t.Fatal("reference revocation was not case-sensitive", err)
	}
	if reflect.DeepEqual(p.Permissions(), after.Permissions()) {
		t.Fatal("review view omitted secret authority")
	}
}

func TestSecretPermissionsRejectMixedOtherKindsAndPreserveRejectedSlices(t *testing.T) {
	p, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	grant := secretPermissionAction("allow_secret", "hello", "second-ref")
	granted, _, err := p.EditPermissions([]PermissionAction{grant})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := granted.CanonicalBytes()
	binding := cdnBindingFixture("public")
	registry := &Registry{Host: "registry.example", RepositoryPrefixes: []string{"demo"}}
	for _, mixed := range []PermissionAction{
		{Kind: AllowDomain, Domain: "demo.example", Secret: grant.Secret},
		{Kind: RevokeDomain, Domain: "demo.example", Secret: grant.Secret},
		{Kind: AllowRegistry, Registry: registry, Secret: grant.Secret},
		{Kind: RevokeRegistry, Registry: registry, Secret: grant.Secret},
		{Kind: AllowCDNBinding, CDNBinding: &binding, Secret: grant.Secret},
		{Kind: RevokeCDNBinding, CDNBinding: &binding, Secret: grant.Secret},
	} {
		if _, _, err := granted.EditPermissions([]PermissionAction{mixed}); err == nil {
			t.Fatal("another action kind accepted a secret payload", mixed.Kind)
		}
	}
	revoke := secretPermissionAction("revoke_secret", "hello", "hello-token")
	if _, _, err := granted.EditPermissions([]PermissionAction{revoke, revoke}); err == nil {
		t.Fatal("repeated revocation accepted")
	}
	after, _ := granted.CanonicalBytes()
	if !bytes.Equal(before, after) || granted.CheckSecret("hello", "hello-token") != nil {
		t.Fatal("rejected revocation mutated shared secret slices")
	}
}
