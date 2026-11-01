package diagnose

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

type udpStore struct {
	fakeStore
	state      plan.BrineState
	allocation plan.UDPAllocation
}

func (s udpStore) LoadBrineState(context.Context, target.Identity, uint64) (plan.BrineState, error) {
	return s.state, nil
}
func (s udpStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{UDP: &plan.UDPPlan{Next: &s.allocation}}, policy.Desired{}, nil
}

func TestUDPDiagnosticReaderUsesTypedStatusAndReservationDiscovery(t *testing.T) {
	reader := fixtureReader()
	snapshot := fixtureSnapshot()
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}, ContainerPort: 7777, ClientAddress: "game.example.com", Exposure: spec.UDPPublic, GrantHash: "sha256:" + strings.Repeat("c", 64)}
	incarnation := "sha256:" + strings.Repeat("d", 64)
	state := plan.BrineState{UDP: &plan.UDPState{Incarnations: []plan.AppIncarnation{{App: "demo", ID: incarnation}}, Reservations: []plan.UDPReservation{{ID: "udp-op", App: "demo", Incarnation: incarnation, State: "active", Endpoint: allocation.Endpoint}, {ID: "udp-staged", App: "staged", Incarnation: incarnation, State: "staged", Owner: "op-stage", Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4434}}}}}
	active := target.Known(true)
	(*snapshot.Apps.Value)[0].UnitActive = &active
	(*snapshot.Apps.Value)[0].UDPPublication = target.Known(target.UDPPublication{BindAddress: "203.0.113.10", HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"})
	snapshot.UDPSockets = target.Known([]target.UDPSocket{})
	snapshot.LocalAddresses = target.Known([]string{"203.0.113.10"})
	control := reader.Store.(fakeStore)
	control.release.Units = *(*snapshot.Apps.Value)[0].QuadletUnits.Value
	reader.Store = udpStore{control, state, allocation}
	reader.Inventory = fakeInventory{snapshot: snapshot}
	reader.Runner = nil
	report, err := reader.Read(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Apps) != 2 || report.Apps[1].Name != "staged" {
		t.Fatal(report.AppNames, report.Apps)
	}
	if report.Apps[0].UDP == nil || !slices.Contains(report.Apps[0].UDP.LocalReasons, "socket_missing") {
		t.Fatal(report.Apps[0])
	}
	codes := []string{}
	for _, finding := range report.Findings {
		codes = append(codes, finding.Code)
	}
	if !slices.Contains(codes, "udp_socket_missing") || !slices.Contains(codes, "udp_operator_network") {
		t.Fatal(codes)
	}
	raw, _ := json.Marshal(report)
	if _, err = DecodeReport(raw); err != nil {
		t.Fatal(err, string(raw))
	}
	corrupted := strings.Replace(string(raw), `"remote":"unverified"`, `"remote":"verified"`, 1)
	if _, err = DecodeReport([]byte(corrupted)); err == nil {
		t.Fatal("accepted fabricated remote proof")
	}
}

func TestUDPDiagnosticReasonRulesAreClosedAndReadOnly(t *testing.T) {
	for _, reason := range []string{"mapping_missing", "mapping_mismatch", "network_mode_unsupported", "socket_missing", "socket_foreign", "bind_address_absent", "observation_unknown"} {
		t.Run(reason, func(t *testing.T) { assertUDPReasonRule(t, reason) })
	}
	report := Report{Apps: []App{{Name: "demo", UDPRemnants: []plan.UDPEndpoint{{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}}}}}
	findings := Findings(report)
	if len(findings) != 2 || findings[0].Code != "udp_remnant" || findings[1].Code != "udp_operator_network" {
		t.Fatal(findings)
	}
}

func assertUDPReasonRule(t *testing.T, reason string) {
	t.Helper()
	report := Report{Apps: []App{{Name: "demo", UDP: &apps.UDPStatus{Protocol: "udp", Local: udpready.NotReady, LocalReasons: []string{reason}, Remote: "unverified"}}}}
	findings := Findings(report)
	if len(findings) != 2 || findings[0].Code != "udp_"+reason || findings[1].Code != "udp_operator_network" {
		t.Fatal(findings)
	}
	for _, finding := range findings {
		assertReadOnlyUDPAdvice(t, finding)
	}
}
func assertReadOnlyUDPAdvice(t *testing.T, finding Finding) {
	t.Helper()
	for _, next := range finding.NextOperations {
		if !strings.HasPrefix(next, "brine status ") && !strings.HasPrefix(next, "brine logs ") {
			t.Fatal("unsafe advice", finding)
		}
	}
}

func (s fakeStore) LoadBrineState(context.Context, target.Identity, uint64) (plan.BrineState, error) {
	return plan.BrineState{}, s.err
}

func (s udpStore) CurrentRelease(ctx context.Context, name string) (ops.Release, error) {
	if name == "staged" {
		return ops.Release{}, store.ErrNotFound
	}
	return s.fakeStore.CurrentRelease(ctx, name)
}

type retiredUDPStore struct{ udpStore }

func (s retiredUDPStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{UDP: &plan.UDPPlan{Retire: &plan.UDPReservation{Endpoint: s.allocation.Endpoint}}}, policy.Desired{}, nil
}
func TestUDPDiagnosticReaderFindsRetiredEndpointRemnant(t *testing.T) {
	reader := fixtureReader()
	control := reader.Store.(fakeStore)
	allocation := plan.UDPAllocation{Endpoint: plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}}
	reader.Store = retiredUDPStore{udpStore{fakeStore: control, allocation: allocation, state: plan.BrineState{UDP: &plan.UDPState{}}}}
	snapshot := fixtureSnapshot()
	snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: "::", Port: 4433}})
	reader.Inventory = fakeInventory{snapshot: snapshot}
	reader.Runner = nil
	report, err := reader.Read(context.Background(), Request{App: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Apps[0].UDPRemnants) != 1 || report.Apps[0].UDP != nil {
		t.Fatal(report.Apps[0])
	}
	codes := []string{}
	for _, finding := range report.Findings {
		codes = append(codes, finding.Code)
	}
	if !slices.Contains(codes, "udp_remnant") {
		t.Fatal(report.Findings)
	}
	raw, _ := json.Marshal(report)
	if _, err = DecodeReport(raw); err != nil {
		t.Fatal(err, string(raw))
	}
}

type udpHistoryStore struct {
	udpStore
	history    store.UDPHistory
	historyErr error
	removed    bool
}

func (s udpHistoryStore) UDPHistory(context.Context, string) (store.UDPHistory, error) {
	return s.history, s.historyErr
}
func (s udpHistoryStore) CurrentRelease(ctx context.Context, name string) (ops.Release, error) {
	if s.removed {
		return ops.Release{}, store.ErrNotFound
	}
	return s.udpStore.CurrentRelease(ctx, name)
}
func TestReleasedUDPRemnantDiscoveryAndReassignment(t *testing.T) {
	for _, name := range []string{"removed head", "older incarnation", "different live app explains socket", "same live app new incarnation explains socket", "current owner missing bind address", "current owner unknown bind address", "unknown sockets", "truncated history", "history failure", "unknown ownership", "scoped"} {
		t.Run(name, func(t *testing.T) { checkUDPHistoricalRemnant(t, name) })
	}
}
func checkUDPHistoricalRemnant(t *testing.T, name string) {
	t.Helper()
	reader := fixtureReader()
	endpoint := plan.UDPEndpoint{Family: "ipv4", BindAddress: "203.0.113.10", HostPort: 4433}
	snapshot := fixtureSnapshot()
	snapshot.UDPSockets = target.Known([]target.UDPSocket{{Address: endpoint.BindAddress, Port: 4433}})
	state := plan.BrineState{UDP: &plan.UDPState{Incarnations: []plan.AppIncarnation{}, Reservations: []plan.UDPReservation{}}}
	history := store.UDPHistory{Endpoints: []plan.UDPEndpoint{endpoint}, Complete: true}
	source := udpHistoryStore{udpStore: udpStore{fakeStore: reader.Store.(fakeStore), state: state}, history: history, removed: true}
	switch name {
	case "older incarnation":
		source.removed = false
	case "different live app explains socket", "same live app new incarnation explains socket", "current owner missing bind address", "current owner unknown bind address":
		app := "other"
		if name == "same live app new incarnation explains socket" {
			app = "demo"
		}
		allocation := plan.UDPAllocation{Endpoint: endpoint, ContainerPort: 7777}
		id := "sha256:" + strings.Repeat("d", 64)
		active := target.Known(true)
		units := []target.Unit{{Name: app + ".container", Hash: id}}
		snapshot.Apps = target.Known([]target.App{{Name: app, UnitActive: &active, QuadletUnits: target.Known(units), UDPPublication: target.Known(target.UDPPublication{BindAddress: endpoint.BindAddress, HostPort: 4433, ContainerPort: 7777, NetworkMode: "pasta"})}})
		snapshot.LocalAddresses = target.Known([]string{endpoint.BindAddress})
		(*snapshot.UDPSockets.Value)[0] = target.UDPSocket{Address: endpoint.BindAddress, Port: 4433, App: app, Process: "pasta", Unit: app + ".service"}
		source.state.UDP.Incarnations = []plan.AppIncarnation{{App: app, ID: id}}
		source.state.UDP.Reservations = []plan.UDPReservation{{ID: "udp-new", App: app, Incarnation: id, State: "active", Endpoint: endpoint}}
		source.state.Releases = []plan.CurrentRelease{{App: app, Units: units, UDP: &allocation}}
		if name == "current owner missing bind address" {
			snapshot.LocalAddresses = target.Known([]string{})
		}
		if name == "current owner unknown bind address" {
			snapshot.LocalAddresses = target.Observation[[]string]{Status: target.Unknown}
		}
	case "unknown sockets":
		snapshot.UDPSockets = target.Observation[[]target.UDPSocket]{Status: target.Unknown}
	case "truncated history":
		source.history.Complete = false
	case "history failure":
		source.historyErr = store.ErrInvalid
	case "unknown ownership":
		source.state.UDP = nil
	}
	reader.Store = source
	reader.Inventory = fakeInventory{snapshot: snapshot}
	reader.Runner = nil
	report, err := reader.Read(t.Context(), Request{App: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	a := report.Apps[0]
	if len(report.Apps) != 1 || a.Name != "demo" {
		t.Fatal("scope lost", report)
	}
	want := name == "removed head" || name == "older incarnation" || name == "scoped"
	if (len(a.UDPRemnants) > 0) != want {
		t.Fatalf("remnants=%+v, want=%v", a, want)
	}
	if strings.HasPrefix(name, "unknown") || name == "truncated history" || name == "history failure" {
		if a.UDP == nil || !slices.Contains(a.UDP.LocalReasons, "observation_unknown") {
			t.Fatal("lost unknown history evidence", a)
		}
	}
}

func (s fakeStore) UDPHistory(context.Context, string) (store.UDPHistory, error) {
	return store.UDPHistory{Endpoints: []plan.UDPEndpoint{}, Complete: true}, s.err
}
