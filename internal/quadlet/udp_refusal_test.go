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
