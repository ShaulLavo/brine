package apps

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

func udpFixture(t *testing.T) (Service, *fakeStore, target.Snapshot) {
	t.Helper()
	service, state, snapshot := fixture(t)
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}, ContainerPort: 7777, ClientAddress: "game.example.com", Exposure: spec.UDPPublic, GrantHash: "sha256:" + strings.Repeat("c", 64)}
	incarnation := "sha256:" + strings.Repeat("d", 64)
	state.state.Releases[0].UDP = &allocation
	state.state.UDP = &plan.UDPState{Incarnations: []plan.AppIncarnation{{App: "hello", ID: incarnation}}, Reservations: []plan.UDPReservation{{ID: "udp-op", App: "hello", Incarnation: incarnation, State: "active", Endpoint: allocation.Endpoint}}}
	a := &(*snapshot.Apps.Value)[0]
	active := target.Known(true)
	a.UnitActive = &active
	a.UDPPublication = target.Known(target.UDPPublication{BindAddress: allocation.Endpoint.BindAddress, HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"})
	snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: allocation.Endpoint.BindAddress, Port: 4433, App: "hello", Unit: "hello.service", Process: "pasta"}})
	snapshot.LocalAddresses = target.Known([]string{allocation.Endpoint.BindAddress})
	service.Inventory = inventory{snapshot}
	return service, state, snapshot
}

func TestUDPStatusSeparatesHTTPAndLocalReadiness(t *testing.T) {
	cases := []struct {
		name, local, reason string
		mutate              func(*target.Snapshot)
	}{
		{"ready", "ready", "", func(*target.Snapshot) {}},
		{"mapping_missing", "not_ready", "mapping_missing", func(s *target.Snapshot) {
			(*s.Apps.Value)[0].UDPPublication = target.Observation[target.UDPPublication]{Status: target.Absent}
		}},
		{"mapping_mismatch", "not_ready", "mapping_mismatch", func(s *target.Snapshot) { (*s.Apps.Value)[0].UDPPublication.Value.ContainerPort++ }},
		{"network_mode", "not_ready", "network_mode_unsupported", func(s *target.Snapshot) { (*s.Apps.Value)[0].UDPPublication.Value.NetworkMode = "slirp4netns" }},
		{"socket_missing", "not_ready", "socket_missing", func(s *target.Snapshot) { s.UDPSockets = target.Known([]target.UDPSocket{}) }},
		{"socket_foreign", "not_ready", "socket_foreign", func(s *target.Snapshot) { (*s.UDPSockets.Value)[0].App = "" }},
		{"bind_missing", "not_ready", "bind_address_absent", func(s *target.Snapshot) { s.LocalAddresses = target.Known([]string{}) }},
		{"unknown", "unknown", "observation_unknown", func(s *target.Snapshot) {
			s.UDPSockets = target.Observation[[]target.UDPSocket]{Status: target.Unknown}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkUDPStatus(t, tc.local, tc.reason, tc.mutate) })
	}
}

func checkUDPStatus(t *testing.T, local, reason string, mutate func(*target.Snapshot)) {
	t.Helper()
	service, _, snapshot := udpFixture(t)
	mutate(&snapshot)
	service.Inventory = inventory{snapshot}
	report, err := service.Status(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	item := report.Apps[0]
	if item.UDP == nil || string(item.UDP.Local) != local || item.UDP.Remote != "unverified" {
		t.Fatalf("%+v", item)
	}
	if reason != "" && !slices.Contains(item.UDP.LocalReasons, reason) {
		t.Fatal(item.UDP)
	}
	if item.Health.Direct != "healthy" {
		t.Fatalf("UDP changed direct HTTP health: %+v", item.Health)
	}
	if item.Ready() != (local == "ready") {
		t.Fatal("overall readiness ignored UDP", item)
	}
	if item.UDP.Reservations[0].ID != "udp-op" || item.UDP.Incarnation == "" || item.Current.UDP == nil {
		t.Fatal(item)
	}
}

func TestUDPStoppedKeepsReservationWithoutListener(t *testing.T) {
	service, _, snapshot := udpFixture(t)
	inactive := target.Known(false)
	(*snapshot.Apps.Value)[0].UnitActive = &inactive
	(*snapshot.Apps.Value)[0].UDPPublication = target.Observation[target.UDPPublication]{Status: target.Absent}
	snapshot.UDPSockets = target.Known([]target.UDPSocket{})
	service.Inventory = inventory{snapshot}
	report, err := service.Status(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	item := report.Apps[0]
	if item.UDP.Local != "stopped" || len(item.UDP.LocalReasons) != 0 || item.UDP.Reservation != "active" || item.Ready() || slices.Contains(item.Drift.Fields, "udp") {
		t.Fatal(item)
	}
}

func TestUDPReservationOnlyDiscoveryAndIncarnationRefusal(t *testing.T) {
	service, state, _ := udpFixture(t)
	state.state.Releases = nil
	state.state.UDP.Reservations[0].State = "staged"
	state.state.UDP.Reservations[0].Owner = "op-stage"
	report, err := service.Status(context.Background(), "hello")
	if err != nil || len(report.Apps) != 1 {
		t.Fatal(report, err)
	}
	item := report.Apps[0]
	if item.Current != nil || item.UDP.Local != "unknown" || item.UDP.Owner != "op-stage" || item.Ready() {
		t.Fatal(item)
	}
	service, state, _ = udpFixture(t)
	state.state.UDP.Incarnations[0].ID = "sha256:" + strings.Repeat("e", 64)
	report, err = service.Status(context.Background(), "hello")
	if err != nil || report.Apps[0].UDP.Local == "ready" {
		t.Fatal(report, err)
	}
}

func TestUDPWireClosedReasonsAndHTTPBytes(t *testing.T) {
	service, state, _ := udpFixture(t)
	state.previous.ID = ""
	state.state.Releases[0].PlanID = "sha256:" + strings.Repeat("a", 64)
	report, err := service.Status(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if _, err = DecodeReport(raw); err != nil {
		t.Fatal(err, string(raw))
	}
	for _, replacement := range []string{`"remote":"verified"`, `"local":"healthy"`, `"local_reasons":["invented"]`, `"local_reasons":null`} {
		field := strings.Split(replacement, ":")[0]
		original := map[string]string{`"remote"`: `"remote":"unverified"`, `"local"`: `"local":"ready"`, `"local_reasons"`: `"local_reasons":[]`}[field]
		if _, err = DecodeReport([]byte(strings.Replace(string(raw), original, replacement, 1))); err == nil {
			t.Fatal("accepted", replacement)
		}
	}
	http, _, _ := fixture(t)
	plain, err := http.Status(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(plain)
	if strings.Contains(string(raw), `"udp"`) {
		t.Fatal("HTTP-only payload changed", string(raw))
	}
}

func TestUDPLocalReadyStillRequiresHTTPHealth(t *testing.T) {
	service, _, _ := udpFixture(t)
	service.Probe = probeFunc(func(context.Context, target.Port, policy.Health) (bool, error) { return false, nil })
	report, err := service.Status(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	item := report.Apps[0]
	if item.UDP.Local != "ready" || item.Health.Direct != "unhealthy" || item.Ready() {
		t.Fatal(item)
	}
}

func TestUDPDriftIncludesOnlyProvenMappingDifferences(t *testing.T) {
	service, state, snapshot := udpFixture(t)
	(*snapshot.Apps.Value)[0].UDPPublication.Value.HostPort++
	snapshot.LocalAddresses = target.Observation[[]string]{Status: target.Unknown}
	service.Inventory = inventory{snapshot}
	report, err := service.Status(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Apps[0].Drift.Fields, []string{"udp"}) {
		t.Fatal(report.Apps[0].Drift)
	}
	state.state.Releases[0].UDP = nil
	state.state.UDP = nil
	report, err = service.Status(context.Background(), "hello")
	if err != nil || !slices.Equal(report.Apps[0].Drift.Fields, []string{"udp"}) {
		t.Fatal(report, err)
	}
}

func TestUDPWireCannotHideRequiredReadiness(t *testing.T) {
	service, state, _ := udpFixture(t)
	state.previous.ID = ""
	state.state.Releases[0].PlanID = "sha256:" + strings.Repeat("a", 64)
	report, err := service.Status(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	report.Apps[0].UDP = nil
	raw, _ := json.Marshal(report)
	if _, err = DecodeReport(raw); err == nil || report.Apps[0].Ready() {
		t.Fatal("missing UDP evidence became ready", string(raw))
	}
}
