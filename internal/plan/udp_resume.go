package plan

import (
	"errors"
	"slices"

	"github.com/ShaulLavo/brine/internal/target"
)

// UnapplyUDP removes only the exact, still-uncommitted preparation of this
// operation family. Foreign reservations and all runtime observations survive.
func UnapplyUDP(s BrineState, p Plan, family []string) (BrineState, bool, error) {
	if p.UDP == nil || s.UDP == nil {
		return s, false, nil
	}
	u := p.UDP
	out := *s.UDP
	out.Reservations = []UDPReservation{}
	claim, retire := false, false
	for _, row := range s.UDP.Reservations {
		if !slices.Contains(family, row.Owner) {
			out.Reservations = append(out.Reservations, row)
			continue
		}
		if row.App != p.App || row.Incarnation != p.ResolvedIncarnation() {
			return s, false, errors.New("UDP preparation identity differs")
		}
		if u.Next != nil && u.Keep == "" && row.State == "staged" && row.ID == "udp-"+row.Owner && row.Endpoint == u.Next.Endpoint && !claim {
			claim = true
			continue
		}
		if u.Retire == nil || retire || row.ID != u.Retire.ID || row.Endpoint != u.Retire.Endpoint || row.State != "releasing" {
			return s, false, errors.New("UDP preparation differs")
		}
		retire = true
		out.Reservations = append(out.Reservations, *u.Retire)
	}
	prepared := claim || retire
	if prepared && (claim != (u.Next != nil && u.Keep == "") || retire != (u.Retire != nil)) {
		return s, false, errors.New("partial UDP preparation")
	}
	s.UDP = &out
	return s, prepared, nil
}

// RebuildInput restores the exact baseline before this family prepared UDP.
// Its optional recollected snapshot changes control-derived facts, not runtime evidence.
func RebuildInput(in Input, p Plan, family []string) (Input, bool, error) {
	state, prepared, err := UnapplyUDP(in.State, p, family)
	if err != nil {
		return in, false, err
	}
	in.State = state
	if p.UDP != nil {
		in.UDPExact = nil
		if p.UDP.Exact != nil {
			exact := *p.UDP.Exact
			in.UDPExact = &exact
		}
	}
	if prepared && in.UDPRebuildSnapshot != nil {
		in.Snapshot = *in.UDPRebuildSnapshot
	}
	in.UDPRebuildSnapshot = nil
	return in, prepared, nil
}

// RebuildUDPControl restores collector inputs, not sockets. A claim-only app
// disappears only when it has no historical identity or runtime artifact.
func RebuildUDPControl(c target.ControlInventory, p Plan, family []string) (target.ControlInventory, error) {
	state := BrineState{UDP: &UDPState{Incarnations: []AppIncarnation{}, Reservations: []UDPReservation{}}}
	for _, r := range c.UDPReservations {
		state.UDP.Reservations = append(state.UDP.Reservations, UDPReservation{ID: r.ID, App: r.App, Incarnation: r.Incarnation, State: r.State, Owner: r.Owner, Endpoint: UDPEndpoint{Family: r.Family, BindAddress: r.BindAddress, HostPort: r.HostPort}})
	}
	restored, prepared, err := UnapplyUDP(state, p, family)
	if err != nil || !prepared {
		return c, err
	}
	c.UDPReservations = []target.ControlUDPReservation{}
	for _, r := range restored.UDP.Reservations {
		c.UDPReservations = append(c.UDPReservations, target.ControlUDPReservation{ID: r.ID, App: r.App, Incarnation: r.Incarnation, State: r.State, Owner: r.Owner, Family: r.Endpoint.Family, BindAddress: r.Endpoint.BindAddress, HostPort: r.Endpoint.HostPort})
	}
	apps := make([]target.ControlApp, 0, len(c.Apps))
	for _, a := range c.Apps {
		if a.Name != p.App || !a.NeverReleased {
			apps = append(apps, a)
			continue
		}
		if a.RetiredUDP != nil && a.RetiredUDP.Status == target.KnownStatus && a.RetiredUDP.Value != nil && len(*a.RetiredUDP.Value) == 0 {
			continue
		}
		a.Status = target.Absent
		apps = append(apps, a)
	}
	c.Apps = apps
	return c, nil
}
