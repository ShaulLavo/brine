package plan

import (
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
)

type AppIncarnation struct {
	App string `json:"app"`
	ID  string `json:"id"`
}
type UDPEndpoint struct {
	Family      string      `json:"family"`
	BindAddress string      `json:"bind_address"`
	HostPort    target.Port `json:"host_port"`
}
type UDPAllocation struct {
	Endpoint      UDPEndpoint      `json:"endpoint"`
	ContainerPort target.Port      `json:"container_port"`
	ClientAddress string           `json:"client_address"`
	Exposure      spec.UDPExposure `json:"exposure"`
	GrantHash     string           `json:"grant_hash"`
}
type UDPReservation struct {
	ID          string      `json:"id"`
	App         string      `json:"app"`
	Incarnation string      `json:"incarnation"`
	State       string      `json:"state"`
	Owner       string      `json:"owner,omitempty"`
	Endpoint    UDPEndpoint `json:"endpoint"`
}
type UDPState struct {
	Incarnations []AppIncarnation `json:"incarnations"`
	Reservations []UDPReservation `json:"reservations"`
}
type UDPPlan struct {
	Incarnation string          `json:"incarnation,omitempty"`
	Exact       *UDPEndpoint    `json:"exact,omitempty"`
	Next        *UDPAllocation  `json:"next,omitempty"`
	Keep        string          `json:"keep,omitempty"`
	Retire      *UDPReservation `json:"retire,omitempty"`
}

const (
	ReserveUDP              ChangeKind   = "reserve_udp"
	ReleaseUDP              ChangeKind   = "release_udp"
	UDPDenied               ConflictCode = "udp_denied"
	UDPPortOwned            ConflictCode = "udp_port_owned"
	UDPPortsExhausted       ConflictCode = "udp_ports_exhausted"
	UDPBindUnavailable      ConflictCode = "udp_bind_address_unavailable"
	UDPRuntimeUnsupported   ConflictCode = "udp_runtime_unsupported"
	UDPUnsettled            ConflictCode = "udp_reservation_unsettled"
	UDPExecutionUnavailable ConflictCode = "udp_execution_unavailable"
)

var ErrUDPExecutionUnavailable = errors.New("udp_execution_unavailable")

func (p Plan) ResolvedIncarnation() string {
	if p.UDP == nil {
		return ""
	}
	if p.UDP.Incarnation != "" {
		return p.UDP.Incarnation
	}
	if p.Kind == Create {
		return p.Hash
	}
	return ""
}

// RefuseUDPExecution closes direct consumers while P08-06 is not implemented.
func RefuseUDPExecution(p Plan, d policy.Desired) error {
	if p.UDP != nil || d.UDP != nil || p.Removal != nil && p.Removal.UDP != nil {
		return ErrUDPExecutionUnavailable
	}
	for _, c := range p.Changes {
		if c.UDP != nil || c.Kind == ReserveUDP || c.Kind == ReleaseUDP || c.Quadlet != nil && (c.Quadlet.UDP != nil || c.Quadlet.Desired.UDP != nil) {
			return ErrUDPExecutionUnavailable
		}
	}
	return nil
}
func (e UDPEndpoint) Validate() error {
	a, err := netip.ParseAddr(e.BindAddress)
	if err != nil || a.String() != e.BindAddress || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.Is4In6() || e.HostPort < 1024 || e.HostPort > 65535 {
		return fmt.Errorf("invalid UDP endpoint")
	}
	if a.Is4() && e.Family == "ipv4" || a.Is6() && e.Family == "ipv6" {
		return nil
	}
	return fmt.Errorf("invalid UDP endpoint family")
}
func (e UDPEndpoint) Overlaps(address string, port target.Port) bool {
	if e.HostPort != port {
		return false
	}
	return address == e.BindAddress || address == "*" || address == "::" || address == "0.0.0.0" && e.Family == "ipv4"
}
func udpInvolved(in Input) bool {
	if in.Desired.UDP != nil || in.UDPExact != nil {
		return true
	}
	for _, r := range in.State.Releases {
		if r.App == string(in.Desired.Name) && r.UDP != nil {
			return true
		}
	}
	if in.State.UDP == nil {
		return false
	}
	for _, r := range in.State.UDP.Reservations {
		if r.App == string(in.Desired.Name) {
			return true
		}
	}
	return false
}
func udpCandidates(in Input) []UDPEndpoint {
	out := []UDPEndpoint{}
	if d := in.Desired.UDP; d != nil {
		for port := uint32(d.Grant.HostPorts.Min); port <= uint32(d.Grant.HostPorts.Max); port++ {
			out = append(out, UDPEndpoint{d.Grant.BindFamily(), d.Grant.BindAddress, target.Port(port)})
		}
	}
	if in.UDPExact != nil {
		out = append(out, *in.UDPExact)
	}
	if in.State.UDP != nil {
		for _, r := range in.State.UDP.Reservations {
			if r.App == string(in.Desired.Name) {
				out = append(out, r.Endpoint)
			}
		}
	}
	for _, r := range in.State.Releases {
		if r.App == string(in.Desired.Name) && r.UDP != nil {
			out = append(out, r.UDP.Endpoint)
		}
	}
	return out
}
func udpRelevant(candidates []UDPEndpoint, address string, port target.Port) bool {
	for _, e := range candidates {
		if e.Overlaps(address, port) {
			return true
		}
	}
	return false
}
func projectUDP(in Input) Input {
	if !udpInvolved(in) {
		in.State.UDP = nil
		return in
	}
	candidates := udpCandidates(in)
	projected := &UDPState{Incarnations: []AppIncarnation{}, Reservations: []UDPReservation{}}
	if in.State.UDP == nil {
		return in
	}
	for _, id := range in.State.UDP.Incarnations {
		if id.App == string(in.Desired.Name) {
			projected.Incarnations = append(projected.Incarnations, id)
		}
	}
	for _, r := range in.State.UDP.Reservations {
		if r.App == string(in.Desired.Name) || udpRelevant(candidates, r.Endpoint.BindAddress, r.Endpoint.HostPort) {
			projected.Reservations = append(projected.Reservations, r)
		}
	}
	in.State.UDP = projected
	return in
}
func canonicalUDPState(s *UDPState) error {
	if s == nil {
		return nil
	}
	if s.Incarnations == nil || s.Reservations == nil {
		return fmt.Errorf("UDP state requires arrays")
	}
	slices.SortFunc(s.Incarnations, func(a, b AppIncarnation) int { return strings.Compare(a.App, b.App) })
	for i, v := range s.Incarnations {
		if v.App == "" || !validHash(v.ID) || i > 0 && s.Incarnations[i-1].App == v.App {
			return fmt.Errorf("invalid app incarnation")
		}
	}
	slices.SortFunc(s.Reservations, func(a, b UDPReservation) int { return strings.Compare(a.ID, b.ID) })
	for i, r := range s.Reservations {
		if r.ID == "" || r.App == "" || !validHash(r.Incarnation) || r.Endpoint.Validate() != nil || i > 0 && s.Reservations[i-1].ID == r.ID {
			return fmt.Errorf("invalid UDP reservation")
		}
		if r.State != "active" && r.State != "staged" && r.State != "releasing" || (r.State != "active") != (r.Owner != "") {
			return fmt.Errorf("invalid UDP reservation state")
		}
	}
	return nil
}
func planUDP(in Input, p *Plan, release *CurrentRelease, current *target.App, add func(ConflictCode, string)) {
	if !udpInvolved(in) {
		return
	}
	add(UDPExecutionUnavailable, "udp")
	u := &UDPPlan{Exact: in.UDPExact}
	p.UDP = u
	var active *UDPReservation
	if in.State.UDP == nil {
		add(UnknownFacts, "udp.reservation")
		return
	}
	for _, id := range in.State.UDP.Incarnations {
		if id.App == p.App {
			u.Incarnation = id.ID
		}
	}
	for _, row := range in.State.UDP.Reservations {
		if row.App != p.App {
			continue
		}
		if row.State != "active" {
			add(UDPUnsettled, "udp.reservation")
			return
		}
		if active != nil {
			add(ArtifactDrift, "udp.reservation")
			return
		}
		copy := row
		active = &copy
	}
	if release != nil && u.Incarnation == "" || release == nil && u.Incarnation != "" {
		add(ArtifactDrift, "udp.incarnation")
		return
	}
	if active != nil && (release == nil || active.Incarnation != u.Incarnation || release.UDP == nil || release.UDP.Endpoint != active.Endpoint) {
		add(ArtifactDrift, "udp.reservation")
		return
	}
	if release != nil && release.UDP != nil && active == nil {
		add(ArtifactDrift, "udp.reservation")
		return
	}
	if in.Desired.UDP == nil {
		u.Retire = active
		if active == nil {
			p.UDP = nil
		}
		udpDiff(p, release)
		return
	}
	if release != nil && release.UDP != nil && (current == nil || current.UDPPublication.Status == target.Unknown || current.UDPPublication.Status == target.Unsupported) {
		add(UnknownFacts, "app.udp_publication")
		return
	}
	d := in.Desired.UDP
	if d.Grant.Validate(in.Desired.AppPorts) != nil || d.Grant.App != p.App || d.Exposure != d.Grant.Exposure {
		add(UDPDenied, "udp")
		return
	}
	if in.Snapshot.UDPSockets.Status != target.KnownStatus {
		add(UnknownFacts, "udp_sockets")
		return
	}
	if in.Snapshot.LocalAddresses.Status != target.KnownStatus {
		add(UnknownFacts, "local_addresses")
		return
	}
	if !slices.Contains(*in.Snapshot.LocalAddresses.Value, d.Grant.BindAddress) {
		add(UDPBindUnavailable, "udp.bind_address")
		return
	}
	if in.Snapshot.Versions.Passt.Status != target.KnownStatus {
		add(UDPRuntimeUnsupported, "versions.passt")
		return
	}
	endpoint, ok := chooseUDPEndpoint(in, active, release, current, u, add)
	if !ok {
		return
	}
	u.Next = &UDPAllocation{Endpoint: endpoint, ContainerPort: target.Port(d.ContainerPort), ClientAddress: d.Grant.ClientAddress, Exposure: d.Exposure, GrantHash: d.Grant.Hash()}
	if active != nil && endpoint == active.Endpoint {
		u.Keep = active.ID
	} else {
		u.Retire = active
	}
	udpDiff(p, release)
}
func grantCovers(g policy.UDPGrant, e UDPEndpoint) bool {
	return e.BindAddress == g.BindAddress && e.HostPort >= target.Port(g.HostPorts.Min) && e.HostPort <= target.Port(g.HostPorts.Max) && e.Validate() == nil
}
func chooseUDPEndpoint(in Input, active *UDPReservation, release *CurrentRelease, current *target.App, u *UDPPlan, add func(ConflictCode, string)) (UDPEndpoint, bool) {
	g := in.Desired.UDP.Grant
	var e UDPEndpoint
	if u.Exact != nil {
		e = *u.Exact
		if !grantCovers(g, e) {
			add(UDPDenied, "udp.endpoint")
			return e, false
		}
	} else if active != nil && grantCovers(g, active.Endpoint) {
		e = active.Endpoint
	} else {
		for _, candidate := range udpCandidates(Input{Desired: in.Desired}) {
			if !udpOccupied(in, candidate, active, release, current) {
				return candidate, true
			}
		}
		code := UDPPortsExhausted
		if g.HostPorts.Min == g.HostPorts.Max {
			code = UDPPortOwned
		}
		add(code, "udp.endpoint")
		return e, false
	}
	if udpOccupied(in, e, active, release, current) {
		add(UDPPortOwned, "udp.endpoint")
		return e, false
	}
	return e, true
}
func udpOccupied(in Input, e UDPEndpoint, active *UDPReservation, release *CurrentRelease, current *target.App) bool {
	for _, row := range in.State.UDP.Reservations {
		if row.Endpoint == e && (active == nil || row.ID != active.ID) {
			return true
		}
	}
	for _, socket := range *in.Snapshot.UDPSockets.Value {
		if !e.Overlaps(socket.Address, socket.Port) {
			continue
		}
		if !explainedUDP(socket, e, active, release, current) {
			return true
		}
	}
	return false
}
func explainedUDP(s target.UDPSocket, e UDPEndpoint, row *UDPReservation, release *CurrentRelease, current *target.App) bool {
	if row == nil || release == nil || current == nil || s.App != row.App || s.Address != e.BindAddress || row.Endpoint != e || current.UDPPublication.Status != target.KnownStatus || current.QuadletUnits.Value == nil || !reflect.DeepEqual(*current.QuadletUnits.Value, release.Units) {
		return false
	}
	publication := current.UDPPublication.Value
	return publication.BindAddress == e.BindAddress && publication.HostPort == e.HostPort && publication.ContainerPort == release.UDP.ContainerPort && publication.NetworkMode == "pasta"
}

func udpDecisionFacts(in Input) (*target.Observation[[]target.UDPSocket], *target.Observation[bool]) {
	if !udpInvolved(in) {
		return nil, nil
	}
	sockets := in.Snapshot.UDPSockets
	if sockets.Value != nil {
		candidates := udpCandidates(in)
		filtered := []target.UDPSocket{}
		for _, socket := range *sockets.Value {
			if udpRelevant(candidates, socket.Address, socket.Port) {
				filtered = append(filtered, socket)
			}
		}
		sockets.Value = &filtered
	}
	var present *target.Observation[bool]
	if d := in.Desired.UDP; d != nil {
		o := target.Observation[bool]{Status: in.Snapshot.LocalAddresses.Status}
		if in.Snapshot.LocalAddresses.Value != nil {
			o = target.Known(slices.Contains(*in.Snapshot.LocalAddresses.Value, d.Grant.BindAddress))
		}
		present = &o
	}
	return &sockets, present
}
func udpConfigHash(d policy.Desired, image Image, port target.Port, secrets []SecretBinding, u *UDPPlan) string {
	if u == nil || u.Next == nil {
		return configHash(d, image, port, secrets)
	}
	return hashJSON(struct {
		Base string         `json:"base"`
		UDP  *UDPAllocation `json:"udp"`
	}{configHash(d, image, port, secrets), u.Next})
}

func udpDiff(p *Plan, previous *CurrentRelease) {
	var old, next *UDPAllocation
	if previous != nil {
		old = previous.UDP
	}
	if p.UDP != nil {
		next = p.UDP.Next
	}
	if reflect.DeepEqual(old, next) {
		return
	}
	if p.Diff == nil {
		p.Diff = &ConfigurationDiff{Secrets: []SecretChange{}}
	}
	p.Diff.UDP = &ValueChange[UDPAllocation]{From: old, To: next}
}

func finishUDP(p Plan, in Input, desired, snapshot, state []byte) (Plan, error) {
	if udpInvolved(in) {
		p.Conflicts = append(p.Conflicts, Diagnostic{UDPExecutionUnavailable, "udp"})
	}
	return finish(p, desired, snapshot, state)
}
