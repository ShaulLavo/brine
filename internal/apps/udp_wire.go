package apps

import (
	"net/netip"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

func validUDPAllocation(a plan.UDPAllocation) bool {
	return digestPattern.MatchString(a.GrantHash) && validUDPDestination(a.Endpoint, a.ContainerPort, a.ClientAddress, a.Exposure)
}
func validUDPDestination(endpoint plan.UDPEndpoint, containerPort target.Port, clientAddress string, exposure spec.UDPExposure) bool {
	if endpoint.Validate() != nil || containerPort < 1024 || containerPort > 65535 || exposure != spec.UDPPublic && exposure != spec.UDPTailnet {
		return false
	}
	ip, err := netip.ParseAddr(clientAddress)
	if err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.Is4In6() && ip.String() == clientAddress
	}
	canonical, err := target.CanonicalDomain(clientAddress)
	return err == nil && canonical == clientAddress
}

func (s UDPStatus) Valid(app string) bool {
	if s.Protocol != "udp" || s.Remote != "unverified" || s.Reservations == nil || len(s.Reservations) > 2 || !(udpready.Result{Local: s.Local, Reasons: s.LocalReasons}).Valid() {
		return false
	}
	if s.Incarnation != "" && !digestPattern.MatchString(s.Incarnation) {
		return false
	}
	seen := map[string]bool{}
	for _, row := range s.Reservations {
		if !validUDPReservation(row, app) || seen[row.ID] {
			return false
		}
		seen[row.ID] = true
	}
	if len(s.Reservations) == 1 && (s.Reservation != s.Reservations[0].State || s.Owner != s.Reservations[0].Owner) {
		return false
	}
	if len(s.Reservations) != 1 && (s.Reservation != "" || s.Owner != "") {
		return false
	}
	if s.HostPort == 0 {
		return s.BindAddress == "" && s.ClientAddress == "" && s.ContainerPort == 0 && s.Exposure == "" && s.Local == udpready.Unknown
	}
	endpoint := plan.UDPEndpoint{Family: "ipv6", BindAddress: s.BindAddress, HostPort: s.HostPort}
	ip, err := netip.ParseAddr(s.BindAddress)
	if err == nil && ip.Is4() {
		endpoint.Family = "ipv4"
	}
	if !validUDPDestination(endpoint, s.ContainerPort, s.ClientAddress, s.Exposure) {
		return false
	}
	if s.Local != udpready.Ready && s.Local != udpready.Stopped {
		return true
	}
	return settledUDP(s, plan.UDPAllocation{Endpoint: endpoint})
}

func validUDPReservation(row plan.UDPReservation, app string) bool {
	if row.App != app || !idPattern.MatchString(row.ID) || !digestPattern.MatchString(row.Incarnation) || row.Endpoint.Validate() != nil {
		return false
	}
	switch row.State {
	case "active":
		return row.Owner == ""
	case "staged", "releasing":
		return idPattern.MatchString(row.Owner)
	default:
		return false
	}
}

func validStatusUDP(s Status) bool {
	if s.UDP == nil {
		return s.Current == nil || s.Current.UDP == nil
	}
	if !s.UDP.Valid(s.App) {
		return false
	}
	if s.Current == nil || s.Current.UDP == nil {
		return s.UDP.HostPort == 0
	}
	a := s.Current.UDP
	return s.UDP.BindAddress == a.Endpoint.BindAddress && s.UDP.HostPort == a.Endpoint.HostPort && s.UDP.ContainerPort == a.ContainerPort && s.UDP.ClientAddress == a.ClientAddress && s.UDP.Exposure == a.Exposure
}
