package policy

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestPermissionsEditPreservesEveryOtherPolicyField(t *testing.T) {
	raw, err := os.ReadFile("testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	actions := []PermissionAction{{Kind: AllowRegistry, Registry: &Registry{Host: "registry.example", RepositoryPrefixes: []string{"owned/demo"}}}, {Kind: AllowDomain, Domain: "demo.example"}}
	after, encoded, err := p.EditPermissions(actions)
	if err != nil {
		t.Fatal(err)
	}
	beforeJSON, _ := p.CanonicalBytes()
	afterJSON, _ := after.CanonicalBytes()
	var beforeFields, afterFields map[string]any
	if err = json.Unmarshal(beforeJSON, &beforeFields); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(afterJSON, &afterFields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"allowed_registries", "allowed_domains"} {
		delete(beforeFields, key)
		delete(afterFields, key)
	}
	if !reflect.DeepEqual(beforeFields, afterFields) {
		t.Fatal("permission edit changed non-permission fields")
	}
	roundTrip, err := Parse(encoded)
	if err != nil || roundTrip.Hash() != after.Hash() {
		t.Fatal("invalid policy serialization", err)
	}
	if reflect.DeepEqual(p.Permissions(), after.Permissions()) {
		t.Fatal("permission change lost")
	}
	restored, _, err := after.EditPermissions([]PermissionAction{{Kind: RevokeRegistry, Registry: actions[0].Registry}, {Kind: RevokeDomain, Domain: "demo.example"}})
	if err != nil || restored.Hash() != p.Hash() {
		t.Fatal("inverse permission plan did not restore intent", err)
	}
}

func TestPermissionsRejectOtherCapabilitiesAndAmbiguousChanges(t *testing.T) {
	raw, err := os.ReadFile("testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cases := [][]PermissionAction{nil, {{Kind: "resources", Domain: "demo.example"}}, {{Kind: AllowDomain, Domain: "*.example"}}, {{Kind: RevokeDomain, Domain: "bad/path"}}, {{Kind: AllowRegistry, Registry: &Registry{Host: "registry.example"}}}, {{Kind: AllowRegistry, Domain: "demo.example", Registry: &Registry{Host: "registry.example", RepositoryPrefixes: []string{"demo"}}}}, {{Kind: AllowDomain, Domain: "demo.example"}, {Kind: RevokeDomain, Domain: "demo.example"}}}
	for i, actions := range cases {
		if _, _, err = p.EditPermissions(actions); err == nil {
			t.Fatalf("accepted case %d", i)
		}
	}
}
