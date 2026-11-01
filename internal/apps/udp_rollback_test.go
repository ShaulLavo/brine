package apps

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestUDPRollbackRestoresHistoricalEndpointUnderCurrentGrant(t *testing.T) {
	for _, mode := range []string{"exact", "foreign", "outside grant", "revoked", "earlier incarnation", "selected HTTP"} {
		t.Run(mode, func(t *testing.T) {
			service, s, snap := fixture(t)
			raw, err := os.ReadFile("../policy/testdata/operator.toml")
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.ReplaceAll(raw, []byte("Registry.Example.com:5000"), []byte("ghcr.io"))
			min := "4433"
			if mode == "outside grant" {
				min = "4434"
			}
			if mode != "revoked" {
				raw = append(raw, []byte("\n[[udp_grants]]\napp = \"hello\"\nexposure = \"public\"\nbind_address = \"192.0.2.10\"\nclient_address = \"relay.example.test\"\nhost_ports = { min = "+min+", max = 4435 }\n")...)
			}
			pol, err := policy.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			service.LoadPolicy = func(context.Context) (policy.Policy, error) { return pol, nil }
			desired := s.desired["old-plan"]
			grant := policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "historical.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}
			desired.UDP = &policy.UDPDesired{ContainerPort: 7777, Exposure: spec.UDPPublic, Grant: grant}
			endpoint := plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4433}
			selected := s.plans["old-plan"]
			selected.UDP = &plan.UDPPlan{Incarnation: "sha256:" + strings.Repeat("d", 64), Next: &plan.UDPAllocation{Endpoint: endpoint, ContainerPort: 7777, ClientAddress: grant.ClientAddress, Exposure: grant.Exposure, GrantHash: grant.Hash()}}
			s.plans["old-plan"], s.desired["old-plan"] = selected, desired
			currentEndpoint := endpoint
			currentEndpoint.HostPort = 4434
			allocation := *selected.UDP.Next
			allocation.Endpoint = currentEndpoint
			s.state.Releases[0].UDP = &allocation
			incarnation := "sha256:" + strings.Repeat("c", 64)
			s.state.UDP = &plan.UDPState{Incarnations: []plan.AppIncarnation{{App: "hello", ID: incarnation}}, Reservations: []plan.UDPReservation{{ID: "udp-current", App: "hello", Incarnation: incarnation, State: "active", Endpoint: currentEndpoint}}}
			if mode == "earlier incarnation" {
				selected.UDP.Incarnation = "sha256:" + strings.Repeat("f", 64)
				s.plans["old-plan"] = selected
			}
			if mode == "selected HTTP" {
				selected.UDP = nil
				desired.UDP = nil
				s.plans["old-plan"], s.desired["old-plan"] = selected, desired
			}
			snap.LocalAddresses = target.Known([]string{"192.0.2.10"})
			snap.UDPSockets = target.Known([]target.UDPSocket{})
			if mode == "foreign" {
				snap.UDPSockets = target.Known([]target.UDPSocket{{Address: endpoint.BindAddress, Port: endpoint.HostPort}})
			}
			(*snap.Apps.Value)[0].UDPPublication = target.Known(target.UDPPublication{BindAddress: currentEndpoint.BindAddress, HostPort: currentEndpoint.HostPort, ContainerPort: 7777, NetworkMode: "pasta"})
			service.Inventory = inventory{snap}
			_, err = service.Rollback(t.Context(), "hello", "")
			if mode == "revoked" {
				if err == nil {
					t.Fatal("revoked historical grant admitted")
				}
				return
			}
			if err != nil || len(s.saved) != 1 {
				t.Fatal(err, len(s.saved))
			}
			p := s.saved[0]
			if p.UDP == nil || p.UDP.Incarnation != incarnation {
				t.Fatal(p.UDP)
			}
			if mode == "selected HTTP" {
				if p.UDP.Next != nil {
					t.Fatal("historical HTTP allocated UDP")
				}
				return
			}
			if p.UDP.Exact == nil || *p.UDP.Exact != endpoint {
				t.Fatal("historical endpoint changed", p.UDP)
			}
			if mode == "foreign" {
				if !slices.Contains(p.Conflicts, plan.Diagnostic{Code: plan.UDPPortOwned, Field: "udp.endpoint"}) {
					t.Fatal(p.Conflicts)
				}
				return
			}
			if mode == "outside grant" {
				if !slices.Contains(p.Conflicts, plan.Diagnostic{Code: plan.UDPDenied, Field: "udp.endpoint"}) {
					t.Fatal(p.Conflicts)
				}
				return
			}
			if p.Kind == plan.Conflict || p.UDP.Next == nil || p.UDP.Next.Endpoint != endpoint || p.UDP.Next.ContainerPort != 7777 || p.UDP.Next.ClientAddress != "relay.example.test" {
				t.Fatal(p.Conflicts, p.UDP)
			}
		})
	}
}
