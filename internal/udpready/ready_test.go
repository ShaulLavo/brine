package udpready

import (
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func readyFixture() (target.Snapshot, plan.UDPAllocation, []target.Unit) {
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}, ContainerPort: 7777}
	units := []target.Unit{{Name: "game.container", Hash: "sha256:" + strings.Repeat("a", 64)}}
	active := target.Known(true)
	snapshot := target.Snapshot{Apps: target.Known([]target.App{{Name: "game", UnitActive: &active, QuadletUnits: target.Known(slices.Clone(units)), UDPPublication: target.Known(target.UDPPublication{BindAddress: "203.0.113.10", HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"})}}), LocalAddresses: target.Known([]string{"203.0.113.10"}), UDPSockets: target.Known([]target.UDPSocket{{Address: "203.0.113.10", Port: 4433, App: "game", Unit: "game.service", Process: "pasta"}})}
	return snapshot, allocation, units
}

func TestUDPReadyOwnershipCannotComeFromTCPOrNamesAlone(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*target.Snapshot)
	}{
		{"tcp_only", func(s *target.Snapshot) {
			s.UDPSockets = target.Known([]target.UDPSocket{})
			s.PortOwners = target.Known([]target.PortOwner{{Port: 4433, App: "game", Unit: "game.service", Process: "pasta"}})
		}},
		{"wildcard_ipv4", func(s *target.Snapshot) { (*s.UDPSockets.Value)[0].Address = "0.0.0.0" }},
		{"wildcard_ipv6", func(s *target.Snapshot) { (*s.UDPSockets.Value)[0].Address = "::" }},
		{"dual_stack", func(s *target.Snapshot) { (*s.UDPSockets.Value)[0].Address = "*" }},
		{"second_socket", func(s *target.Snapshot) {
			*s.UDPSockets.Value = append(*s.UDPSockets.Value, target.UDPSocket{Address: "::", Port: 4433})
		}},
		{"foreign_unit", func(s *target.Snapshot) { (*s.UDPSockets.Value)[0].Unit = "other.service" }},
		{"missing_process", func(s *target.Snapshot) { (*s.UDPSockets.Value)[0].Process = "" }},
		{"unit_changed", func(s *target.Snapshot) {
			(*(*s.Apps.Value)[0].QuadletUnits.Value)[0].Hash = "sha256:" + strings.Repeat("b", 64)
		}},
		{"active_unknown", func(s *target.Snapshot) { (*s.Apps.Value)[0].UnitActive = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertNotReady(t, tc.mutate) })
	}
}
func assertNotReady(t *testing.T, mutate func(*target.Snapshot)) {
	t.Helper()
	snapshot, allocation, units := readyFixture()
	mutate(&snapshot)
	result := Check(snapshot, "game", allocation, units)
	if result.Local == Ready || !result.Valid() {
		t.Fatal(result)
	}
}

func TestUDPReadyIgnoresUnrelatedAddressAndTCP(t *testing.T) {
	snapshot, allocation, units := readyFixture()
	*snapshot.UDPSockets.Value = append(*snapshot.UDPSockets.Value, target.UDPSocket{Address: "203.0.113.11", Port: 4433}, target.UDPSocket{Address: "::", Port: 4434})
	snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 4433, App: "other"}})
	result := Check(snapshot, "game", allocation, units)
	if result.Local != Ready || !result.Valid() {
		t.Fatal(result)
	}
	allocation.Endpoint = plan.UDPEndpoint{Family: "ipv6", BindAddress: "2001:db8::10", HostPort: 4433}
	(*snapshot.Apps.Value)[0].UDPPublication.Value.BindAddress = allocation.Endpoint.BindAddress
	snapshot.LocalAddresses = target.Known([]string{allocation.Endpoint.BindAddress})
	snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: allocation.Endpoint.BindAddress, Port: 4433, App: "game", Unit: "game.service", Process: "pasta"}, {Address: "0.0.0.0", Port: 4433}})
	if result = Check(snapshot, "game", allocation, units); result.Local != Ready {
		t.Fatal(result)
	}
}
