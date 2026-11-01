package quadlet

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestUDPIntentRefusesBeforeRendering(t *testing.T) {
	d, _ := fixture(t)
	d.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.1", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}
	_, err := Render(d, bind(t, d), manifest())
	if err == nil || !strings.Contains(err.Error(), "udp_execution_unavailable") {
		t.Fatalf("UDP intent rendered as HTTP: %v", err)
	}
}

func TestUninvolvedHTTPUnitIgnoresUDPPolicyOnlyHashChange(t *testing.T) {
	d, p := fixture(t)
	before, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	d.PolicyHash = "sha256:" + strings.Repeat("c", 64)
	p = bind(t, d)
	p.Hash = "sha256:" + strings.Repeat("d", 64)
	after, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	_, a, _ := strings.Cut(string(before.Bytes()), "\n")
	_, b, _ := strings.Cut(string(after.Bytes()), "\n")
	if a != b {
		t.Fatal("an uninvolved HTTP unit changed beyond its plan marker")
	}
}
