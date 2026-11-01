package planview

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/ui"
)

func TestUDPHumanEndpointsAreUnambiguous(t *testing.T) {
	p := plan.Plan{Kind: plan.Conflict, App: "relay", UDP: &plan.UDPPlan{Next: &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv6", BindAddress: "2001:db8::1", HostPort: 4433}, ContainerPort: 4434, ClientAddress: "2001:db8::2", Exposure: spec.UDPPublic}}}
	got := Human(p, ui.NewTheme(true), 80)
	if !strings.Contains(got, "[2001:db8::1]:4433") || !strings.Contains(got, "[2001:db8::2]:4433") {
		t.Fatal(got)
	}
}
