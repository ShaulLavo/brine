package apps

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/udpready"
)

type UDPStatus struct {
	Protocol      string                `json:"protocol"`
	Exposure      spec.UDPExposure      `json:"exposure,omitempty"`
	BindAddress   string                `json:"bind_address,omitempty"`
	ClientAddress string                `json:"client_address,omitempty"`
	HostPort      target.Port           `json:"host_port,omitempty"`
	ContainerPort target.Port           `json:"container_port,omitempty"`
	Incarnation   string                `json:"incarnation,omitempty"`
	Reservation   string                `json:"reservation,omitempty"`
	Owner         string                `json:"owner,omitempty"`
	Reservations  []plan.UDPReservation `json:"reservations"`
	Local         udpready.State        `json:"local"`
	LocalReasons  []string              `json:"local_reasons"`
	Remote        string                `json:"remote"`
}

func (s Status) Ready() bool {
	if s.Current == nil || s.Health.Direct != "healthy" || s.Health.UnitActive.Status != target.KnownStatus || s.Health.UnitActive.Value == nil || !*s.Health.UnitActive.Value {
		return false
	}
	if s.Current.UDP != nil && s.UDP == nil {
		return false
	}
	return s.UDP == nil || s.UDP.Local == udpready.Ready
}

// UDPStatusFor joins measured facts to exact durable reservation and head identity.
func UDPStatusFor(snapshot target.Snapshot, state *plan.UDPState, current plan.CurrentRelease) *UDPStatus {
	out := UDPStatus{Protocol: "udp", Reservations: []plan.UDPReservation{}, Local: udpready.Unknown, LocalReasons: []string{"observation_unknown"}, Remote: "unverified"}
	out.Incarnation, out.Reservations = udpStateFor(state, current.App)
	if current.UDP == nil && len(out.Reservations) == 0 {
		return nil
	}
	slices.SortFunc(out.Reservations, func(a, b plan.UDPReservation) int { return strings.Compare(a.ID, b.ID) })
	if len(out.Reservations) == 1 {
		out.Reservation = out.Reservations[0].State
		out.Owner = out.Reservations[0].Owner
	}
	if current.UDP == nil {
		return &out
	}
	a := current.UDP
	out.Exposure = a.Exposure
	out.BindAddress = a.Endpoint.BindAddress
	out.ClientAddress = a.ClientAddress
	out.HostPort = a.Endpoint.HostPort
	out.ContainerPort = a.ContainerPort
	if !settledUDP(out, *a) {
		return &out
	}
	readiness := udpready.Check(snapshot, current.App, *a, current.Units)
	out.Local = readiness.Local
	out.LocalReasons = readiness.Reasons
	return &out
}

func settledUDP(s UDPStatus, a plan.UDPAllocation) bool {
	if s.Incarnation == "" || len(s.Reservations) != 1 {
		return false
	}
	row := s.Reservations[0]
	return row.State == "active" && row.Owner == "" && row.Incarnation == s.Incarnation && row.Endpoint == a.Endpoint
}

func (s Service) appendUDPReservations(ctx context.Context, snapshot target.Snapshot, state plan.BrineState, app string, report *Report) error {
	if state.UDP == nil {
		return nil
	}
	present := map[string]bool{}
	for _, item := range report.Apps {
		present[item.App] = true
	}
	for _, row := range state.UDP.Reservations {
		if present[row.App] || app != "" && app != row.App {
			continue
		}
		item := Status{App: row.App, Health: Health{UnitActive: target.Observation[bool]{Status: target.Unknown}, Direct: "not_checked"}, Drift: Drift{State: "unknown", Fields: []string{}}, UDP: UDPStatusFor(snapshot, state.UDP, plan.CurrentRelease{App: row.App})}
		op, err := s.Store.LastOperation(ctx, row.App)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err == nil {
			item.LastOperation = &op
		}
		report.Apps = append(report.Apps, item)
		present[row.App] = true
	}
	return nil
}

func httpDrifted(d Drift) bool {
	for _, field := range d.Fields {
		if field != "udp" {
			return true
		}
	}
	return false
}

func udpMappingComparison(a target.App, allocation *plan.UDPAllocation) (bool, bool) {
	o := a.UDPPublication
	if o.Status == target.Absent {
		stopped := a.UnitActive != nil && a.UnitActive.Status == target.KnownStatus && a.UnitActive.Value != nil && !*a.UnitActive.Value
		return true, allocation == nil || stopped
	}
	if o.Status != target.KnownStatus || o.Value == nil {
		return false, false
	}
	if allocation == nil {
		return true, false
	}
	p := o.Value
	return true, p.BindAddress == allocation.Endpoint.BindAddress && p.HostPort == allocation.Endpoint.HostPort && p.ContainerPort == allocation.ContainerPort && p.NetworkMode == "pasta"
}

func udpStateFor(state *plan.UDPState, app string) (string, []plan.UDPReservation) {
	rows := []plan.UDPReservation{}
	if state == nil {
		return "", rows
	}
	incarnation := ""
	for _, head := range state.Incarnations {
		if head.App == app {
			incarnation = head.ID
		}
	}
	for _, row := range state.Reservations {
		if row.App == app {
			rows = append(rows, row)
		}
	}
	return incarnation, rows
}
