package planfile

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestUDPExactOfflineRoundTripStaysNonApplyable(t *testing.T) {
	in := fixture(t)
	in.Desired.UDP = &policy.UDPDesired{ContainerPort: 4433, Exposure: spec.UDPPublic, Grant: policy.UDPGrant{App: "hello", Exposure: spec.UDPPublic, BindAddress: "192.0.2.10", ClientAddress: "relay.example.test", HostPorts: policy.PortRange{Min: 4433, Max: 4435}}}
	in.Snapshot.LocalAddresses = target.Known([]string{"192.0.2.10"})
	in.State.UDP = &plan.UDPState{Incarnations: []plan.AppIncarnation{}, Reservations: []plan.UDPReservation{}}
	in.UDPExact = &plan.UDPEndpoint{Family: "ipv4", BindAddress: "192.0.2.10", HostPort: 4435}
	file, err := New(in, Metadata{CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ToolVersion: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil || got.Hash() != file.Hash() || got.Applyable() || got.Plan().UDP.Exact == nil || *got.Plan().UDP.Exact != *in.UDPExact {
		t.Fatal("UDP offline exact endpoint lost", err)
	}
}
