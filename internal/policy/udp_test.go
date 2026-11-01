package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func udpGrantFixture() UDPGrant {
	return UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: PortRange{Min: 4433, Max: 4433}}
}
func TestUDPGrantValidation(t *testing.T) {
	for _, change := range []func(*UDPGrant){
		func(g *UDPGrant) { g.BindAddress = "0.0.0.0" }, func(g *UDPGrant) { g.BindAddress = "::" }, func(g *UDPGrant) { g.BindAddress = "::ffff:192.0.2.10" }, func(g *UDPGrant) { g.BindAddress = "fe80::1%eth0" }, func(g *UDPGrant) { g.BindAddress = "224.0.0.1" }, func(g *UDPGrant) { g.BindAddress = "2001:0db8::1" },
		func(g *UDPGrant) { g.ClientAddress = "" }, func(g *UDPGrant) { g.ClientAddress = "relay.example.test." }, func(g *UDPGrant) { g.ClientAddress = "Relay.example.test" }, func(g *UDPGrant) { g.HostPorts = PortRange{Min: 80, Max: 80} }, func(g *UDPGrant) { g.HostPorts = PortRange{Min: 2019, Max: 2019} }, func(g *UDPGrant) { g.HostPorts = PortRange{Min: 20000, Max: 20001} }, func(g *UDPGrant) { g.HostPorts = PortRange{Min: 4433, Max: 4497} }, func(g *UDPGrant) { g.Exposure = "private" },
	} {
		g := udpGrantFixture()
		change(&g)
		if g.Validate(PortRange{Min: 20000, Max: 20999}) == nil {
			t.Fatalf("accepted %+v", g)
		}
	}
	g := udpGrantFixture()
	raw, _ := json.Marshal(g)
	parsed, err := ParseUDPGrant(raw)
	if err != nil || parsed != g {
		t.Fatal(parsed, err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"min":4433`, `"min":4433,"min":4433`, 1), strings.Replace(string(raw), `"app":"hello"`, `"App":"hello"`, 1), string(raw) + "{}", strings.Replace(string(raw), `"min":4433`, `"min":null`, 1), strings.Repeat(" ", 4097)} {
		if _, err := ParseUDPGrant([]byte(bad)); err == nil {
			t.Fatal("accepted malformed grant")
		}
	}
}
func TestUDPPolicyNormalizationAndPermissionEdit(t *testing.T) {
	raw, err := os.ReadFile("testdata/operator.toml")
	if err != nil {
		t.Fatal(err)
	}
	pol, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	appRaw, err := os.ReadFile("../spec/testdata/valid-minimal.toml")
	if err != nil {
		t.Fatal(err)
	}
	appRaw = bytes.ReplaceAll(appRaw, []byte("ghcr.io/example/hello"), []byte("registry.example.com:5000/team/hello"))
	app, err := spec.Parse(appRaw)
	if err != nil {
		t.Fatal(err)
	}
	app.UDP = &spec.UDP{ContainerPort: 4433, Exposure: spec.UDPPublic}
	if _, err = Normalize(app, pol); err == nil {
		t.Fatal("UDP admitted without grant")
	}
	g := udpGrantFixture()
	granted, _, err := pol.EditPermissions([]PermissionAction{{Kind: AllowUDPGrant, UDPGrant: &g}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Normalize(app, granted)
	if err != nil {
		t.Fatal(err)
	}
	if d.UDP == nil || !d.Stateless() || d.App().UDP == nil || d.UDP.Grant != g {
		t.Fatal("UDP lost or gained data authority")
	}
	before, _ := d.CanonicalBytes()
	app.UDP.ContainerPort = 5555
	after, _ := d.CanonicalBytes()
	if !bytes.Equal(before, after) {
		t.Fatal("desired aliases app")
	}
	for _, action := range []PermissionAction{{Kind: AllowDomain, Domain: "other.example.test", UDPGrant: &g}, {Kind: AllowUDPGrant, UDPGrant: &g, Domain: "other.example.test"}, {Kind: RevokeUDPGrant, UDPApp: "hello", UDPGrant: &g}} {
		if _, _, err := granted.EditPermissions([]PermissionAction{action}); err == nil {
			t.Fatal("mixed UDP authority admitted")
		}
	}
	app.UDP.Exposure = spec.UDPTailnet
	if _, err = Normalize(app, granted); err == nil {
		t.Fatal("exposure widened")
	}
}
