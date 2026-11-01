package quadlet

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
)

func TestUDPIntentRendersApprovedMapping(t *testing.T) {
	d, _ := fixture(t)
	d.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.1", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}
	p := bind(t, d)
	allocation := &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.1", HostPort: 4433}, ContainerPort: 4433, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic, GrantHash: d.UDP.Grant.Hash()}
	p.UDP = &plan.UDPPlan{Next: allocation}
	p.Changes = []plan.Change{{Kind: plan.RenderQuadlet, Quadlet: &plan.Quadlet{Desired: d, UDP: allocation}}}
	u, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(u.Bytes()), "PublishPort=192.0.2.1:4433:4433/udp\n") {
		t.Fatal("missing UDP mapping", string(u.Bytes()))
	}
	if strings.Contains(string(u.Bytes()), "Network=") || strings.Contains(string(u.Bytes()), "Capability=") || strings.Contains(string(u.Bytes()), "Socket=") {
		t.Fatal("UDP mapping widened the container privileges")
	}
}

func TestUDPIntentRejectsMismatchedAllocation(t *testing.T) {
	d, _ := fixture(t)
	d.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.1", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}
	p := bind(t, d)
	allocation := &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.1", HostPort: 4433}, ContainerPort: 4433, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic, GrantHash: d.UDP.Grant.Hash()}
	p.UDP = &plan.UDPPlan{Next: allocation}
	p.Changes = []plan.Change{{Kind: plan.RenderQuadlet, Quadlet: &plan.Quadlet{Desired: d, UDP: allocation}}}
	for _, mutate := range []func(*plan.UDPAllocation){
		func(a *plan.UDPAllocation) { a.GrantHash = "sha256:" + strings.Repeat("0", 64) },
		func(a *plan.UDPAllocation) { a.Exposure = spec.UDPTailnet },
		func(a *plan.UDPAllocation) { a.ContainerPort = 4434 },
		func(a *plan.UDPAllocation) { a.Endpoint.HostPort = 4434 },
	} {
		mutate(allocation)
		if _, err := Render(d, p, manifest()); err == nil {
			t.Fatal("accepted mismatched UDP allocation")
		}
		allocation = &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.1", HostPort: 4433}, ContainerPort: 4433, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic, GrantHash: d.UDP.Grant.Hash()}
		p.UDP.Next = allocation
		p.Changes[0].Quadlet.UDP = allocation
	}
}

func TestUDPIPv6MappingUsesBracketedAddress(t *testing.T) {
	d, _ := fixture(t)
	d.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "2001:db8::1", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}
	p := bind(t, d)
	allocation := &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv6", BindAddress: "2001:db8::1", HostPort: 4433}, ContainerPort: 4433, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic, GrantHash: d.UDP.Grant.Hash()}
	p.UDP = &plan.UDPPlan{Next: allocation}
	p.Changes = []plan.Change{{Kind: plan.RenderQuadlet, Quadlet: &plan.Quadlet{Desired: d, UDP: allocation}}}
	u, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(u.Bytes()), "PublishPort=[2001:db8::1]:4433:4433/udp\n") {
		t.Fatal("missing bracketed IPv6 mapping", string(u.Bytes()))
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

func TestUDPBindingRejectsForgedInputs(t *testing.T) {
	for _, name := range []string{"client", "grant app", "grant exposure", "invalid grant", "nil next", "nil payload", "duplicate payload", "nil followed by payload", "payload drift", "payload intent", "bad desired port", "bad desired exposure", "family", "wildcard"} {
		t.Run(name, func(t *testing.T) {
			d, _ := fixture(t)
			d.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.1", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4433}}}
			a := &plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.1", HostPort: 4433}, ContainerPort: 4433, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic, GrantHash: d.UDP.Grant.Hash()}
			p := bind(t, d)
			p.UDP = &plan.UDPPlan{Next: a}
			q := &plan.Quadlet{Desired: d, UDP: a}
			p.Changes = []plan.Change{{Kind: plan.RenderQuadlet, Quadlet: q}}
			switch name {
			case "client":
				a.ClientAddress = "other.example.test"
			case "grant app":
				d.UDP.Grant.App = "other"
			case "grant exposure":
				d.UDP.Grant.Exposure = spec.UDPTailnet
			case "invalid grant":
				d.UDP.Grant.HostPorts.Max = 65535
			case "nil next":
				p.UDP.Next = nil
			case "nil payload":
				q.UDP = nil
			case "duplicate payload":
				p.Changes = append(p.Changes, p.Changes[0])
			case "nil followed by payload":
				p.Changes = append([]plan.Change{{Kind: plan.RenderQuadlet, Quadlet: &plan.Quadlet{}}}, p.Changes...)
			case "payload drift":
				copy := *a
				copy.ContainerPort++
				q.UDP = &copy
			case "payload intent":
				q.Desired.UDP = nil
			case "bad desired port":
				d.UDP.ContainerPort = 1
			case "bad desired exposure":
				d.UDP.Exposure = "other"
			case "family":
				a.Endpoint.Family = "ipv6"
			case "wildcard":
				a.Endpoint.BindAddress = "0.0.0.0"
			}
			raw, err := d.CanonicalBytes()
			if err == nil {
				p.DesiredHash = digest(raw)
			}
			a.GrantHash = d.UDP.Grant.Hash()
			if _, err := Render(d, p, manifest()); err == nil {
				t.Fatal("accepted forged UDP binding")
			}
		})
	}
}
