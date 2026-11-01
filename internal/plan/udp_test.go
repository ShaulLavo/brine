package plan

import (
	"bytes"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func udpInput(t *testing.T, existing bool) Input {
	in := fixture(t, "ready-arm64")
	if existing {
		in = installed(t)
	}
	in.Desired.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4435}}}
	in.Snapshot.LocalAddresses = target.Known([]string{"192.0.2.10"})
	in.State.UDP = &UDPState{Incarnations: []AppIncarnation{}, Reservations: []UDPReservation{}}
	if !existing {
		return in
	}
	id := "sha256:" + strings.Repeat("c", 64)
	endpoint := UDPEndpoint{"ipv4", "192.0.2.10", 4433}
	allocation := &UDPAllocation{Endpoint: endpoint, ContainerPort: 4433, ClientAddress: "relay.example.test", Exposure: spec.UDPPublic, GrantHash: in.Desired.UDP.Grant.Hash()}
	in.State.UDP.Incarnations = append(in.State.UDP.Incarnations, AppIncarnation{"hello", id})
	in.State.UDP.Reservations = append(in.State.UDP.Reservations, UDPReservation{ID: "udp-first", App: "hello", Incarnation: id, State: "active", Endpoint: endpoint})
	in.State.Releases[0].UDP = allocation
	in.State.Releases[0].Desired = in.Desired
	app := &(*in.Snapshot.Apps.Value)[0]
	app.UDPPublication = target.Known(target.UDPPublication{BindAddress: endpoint.BindAddress, HostPort: 4433, ContainerPort: 4433, NetworkMode: "pasta"})
	return in
}
func TestUDPTransitions(t *testing.T) {
	for _, mode := range []string{"create", "retain", "container-port", "move", "release", "stopped", "unsettled", "orphan", "wrong-incarnation"} {
		t.Run(mode, func(t *testing.T) {
			in := udpInput(t, mode != "create")
			switch mode {
			case "container-port":
				in.Desired.UDP.ContainerPort = 5555
			case "move":
				in.Desired.UDP.Grant.HostPorts = policy.PortRange{Min: 4434, Max: 4435}
			case "release":
				in.Desired.UDP = nil
			case "stopped":
				(*in.Snapshot.Apps.Value)[0].Image = target.Observation[target.Image]{Status: target.Absent}
				(*in.Snapshot.Apps.Value)[0].UDPPublication = target.Observation[target.UDPPublication]{Status: target.Absent}
			case "unsettled":
				in.State.UDP.Reservations[0].State = "releasing"
				in.State.UDP.Reservations[0].Owner = "other-op"
				in.Desired.UDP = nil
			case "orphan":
				in.State.Releases = []CurrentRelease{}
				in.State.UDP.Incarnations = []AppIncarnation{}
			case "wrong-incarnation":
				in.State.UDP.Reservations[0].Incarnation = "sha256:" + strings.Repeat("d", 64)
			}
			p := build(t, in)
			if mode != "unsettled" && mode != "orphan" && mode != "wrong-incarnation" && p.Kind == Conflict {
				t.Fatal(p.Conflicts)
			}
			if mode == "unsettled" {
				if !slices.Contains(p.Conflicts, Diagnostic{UDPUnsettled, "udp.reservation"}) {
					t.Fatal(p.Conflicts)
				}
				return
			}
			if mode == "orphan" || mode == "wrong-incarnation" {
				if !slices.Contains(p.Conflicts, Diagnostic{ArtifactDrift, "udp.reservation"}) {
					t.Fatal(p.Conflicts)
				}
				return
			}
			if mode == "release" {
				if p.UDP.Retire == nil || p.UDP.Next != nil {
					t.Fatal(p.UDP)
				}
				return
			}
			if p.UDP == nil || p.UDP.Next == nil {
				t.Fatal(p.Conflicts)
			}
			if mode == "move" {
				if p.UDP.Retire == nil || p.UDP.Keep != "" || p.UDP.Next.Endpoint.HostPort != 4434 {
					t.Fatal(p.UDP)
				}
				return
			}
			if mode != "create" && p.UDP.Keep != "udp-first" {
				t.Fatal(p.UDP)
			}
		})
	}
}
func TestUDPOverlapsOwnershipAndExactAllocation(t *testing.T) {
	for _, address := range []string{"192.0.2.10", "0.0.0.0", "::", "*"} {
		t.Run(address, func(t *testing.T) {
			in := udpInput(t, false)
			in.Desired.UDP.Grant.HostPorts.Max = 4433
			in.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: address, Port: 4433, App: "hello"}})
			p := build(t, in)
			if !slices.Contains(p.Conflicts, Diagnostic{UDPPortOwned, "udp.endpoint"}) {
				t.Fatal(p.Conflicts)
			}
		})
	}
	in := udpInput(t, false)
	in.Snapshot.UsedPorts = target.Known([]target.Port{4433})
	in.Snapshot.PortOwners = target.Known([]target.PortOwner{{Port: 4433, App: "other"}})
	if p := build(t, in); p.UDP.Next == nil || p.UDP.Next.Endpoint.HostPort != 4433 {
		t.Fatal("TCP blocked UDP", p.Conflicts)
	}
	in.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4433}})
	if p := build(t, in); p.UDP.Next == nil || p.UDP.Next.Endpoint.HostPort != 4434 {
		t.Fatal("did not skip occupied lowest", p.Conflicts)
	}
	in.UDPExact = &UDPEndpoint{"ipv4", "192.0.2.10", 4433}
	if p := build(t, in); !slices.Contains(p.Conflicts, Diagnostic{UDPPortOwned, "udp.endpoint"}) {
		t.Fatal("exact endpoint substituted", p.Conflicts)
	}
	in.UDPExact.HostPort = 9999
	if p := build(t, in); !slices.Contains(p.Conflicts, Diagnostic{UDPDenied, "udp.endpoint"}) {
		t.Fatal(p.Conflicts)
	}
}
func TestUDPDecisionProjection(t *testing.T) {
	in := udpInput(t, false)
	p := build(t, in)
	in.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 50000}})
	in.Snapshot.LocalAddresses = target.Known([]string{"127.0.0.1", "192.0.2.10"})
	if build(t, in).Hash != p.Hash {
		t.Fatal("unrelated churn staled UDP plan")
	}
	in.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4435}})
	if build(t, in).Hash == p.Hash {
		t.Fatal("in-range socket lost from freshness")
	}
	in = fixture(t, "ready-arm64")
	baseline := build(t, in)
	want, _ := baseline.CanonicalBytes()
	in.State.UDP = udpInput(t, true).State.UDP
	in.State.UDP.Reservations[0].App = "other"
	in.Snapshot.UDPSockets = target.Observation[[]target.UDPSocket]{Status: target.Unknown}
	in.Snapshot.LocalAddresses = target.Observation[[]string]{Status: target.Unknown}
	got, _ := build(t, in).CanonicalBytes()
	if !bytes.Equal(want, got) {
		t.Fatal("uninvolved HTTP plan bytes changed")
	}
}
func TestUDPUnknownObservations(t *testing.T) {
	for _, field := range []string{"udp_sockets", "local_addresses"} {
		t.Run(field, func(t *testing.T) {
			in := udpInput(t, false)
			if field == "udp_sockets" {
				in.Snapshot.UDPSockets = target.Observation[[]target.UDPSocket]{Status: target.Unknown}
			} else {
				in.Snapshot.LocalAddresses = target.Observation[[]string]{Status: target.Unknown}
			}
			p := build(t, in)
			if !slices.Contains(p.Conflicts, Diagnostic{UnknownFacts, field}) {
				t.Fatal(p.Conflicts)
			}
		})
	}
}

func TestUDPRetainedOwnershipNeedsAllProofs(t *testing.T) {
	in := udpInput(t, true)
	in.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4433, App: "hello"}})
	if p := build(t, in); p.UDP.Next == nil || slices.Contains(p.Conflicts, Diagnostic{UDPPortOwned, "udp.endpoint"}) {
		t.Fatal(p.Conflicts)
	}
	(*in.Snapshot.Apps.Value)[0].UDPPublication = target.Observation[target.UDPPublication]{Status: target.Unknown}
	if p := build(t, in); !slices.Contains(p.Conflicts, Diagnostic{UnknownFacts, "app.udp_publication"}) {
		t.Fatal(p.Conflicts)
	}
	in = udpInput(t, false)
	row := udpInput(t, true).State.UDP.Reservations[0]
	row.App = "stopped-other"
	in.State.UDP.Reservations = append(in.State.UDP.Reservations, row)
	if p := build(t, in); p.UDP.Next.Endpoint.HostPort != 4434 {
		t.Fatal("stopped claim reused", p.Conflicts)
	}
}

func TestUDPRemovalAndLifecycle(t *testing.T) {
	for _, action := range []ChangeKind{StartApp, StopApp, RestartApp, RemoveApp} {
		in := udpInput(t, true)
		if action == RemoveApp {
			in.State.Releases[0].Units = in.State.Releases[0].Units[:1]
			(*in.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(in.State.Releases[0].Units)
		}
		p, err := BuildLifecycle(in, action)
		if err != nil || p.Kind != Update || p.Lifecycle != action || len(p.Changes) == 0 {
			t.Fatal(action, p, err)
		}
		if action == RemoveApp && (p.Removal == nil || p.Removal.UDP == nil || p.UDP.Next != nil) {
			t.Fatal("removal did not retire exact reservation", p.UDP)
		}
	}
}

func TestUDPCanonicalGoldens(t *testing.T) {
	for _, mode := range []string{"create", "retain", "move", "release"} {
		in := udpInput(t, mode != "create")
		if mode == "move" {
			in.Desired.UDP.Grant.HostPorts = policy.PortRange{Min: 4434, Max: 4435}
		}
		if mode == "release" {
			in.Desired.UDP = nil
		}
		raw, err := build(t, in).CanonicalBytes()
		if err != nil {
			t.Fatal(err)
		}
		file := "testdata/udp-" + mode + ".json"
		updateUDPFixture(t, file, raw)
		want, err := fs.ReadFile(os.DirFS("testdata"), "udp-"+mode+".json")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, want) {
			t.Fatal("UDP canonical bytes changed", mode)
		}
	}
}

func updateUDPFixture(t *testing.T, file string, raw []byte) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		return
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUDPExactInputCannotMutateHashedPlan(t *testing.T) {
	in := udpInput(t, false)
	in.UDPExact = &UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4434}
	p := build(t, in)
	before, err := p.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	in.UDPExact.HostPort = 9999
	after, err := p.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("UDP exact input aliases the hashed plan")
	}
}

func TestUDPExtraOwnedSocketStillConflicts(t *testing.T) {
	in := udpInput(t, true)
	in.Snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: "192.0.2.10", Port: 4433, App: "hello", Process: "pasta", Unit: "hello.service"}, {Address: "192.0.2.10", Port: 4433, App: "hello", Process: "other", Unit: "hello.service"}})
	p := build(t, in)
	if !slices.Contains(p.Conflicts, Diagnostic{UDPPortOwned, "udp.endpoint"}) {
		t.Fatal("extra overlapping owned socket accepted", p.Conflicts)
	}
}
