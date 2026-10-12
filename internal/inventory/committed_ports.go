package inventory

import "github.com/ShaulLavo/brine/internal/target"

func attachCommittedPorts(s *target.Snapshot, control *target.ControlInventory) {
	if control == nil || control.Target == nil || *control.Target != s.Identity || s.Apps.Value == nil || s.Apps.Status != target.KnownStatus {
		return
	}
	reservations := map[string]*target.CommittedPortReservation{}
	for _, app := range control.Apps {
		if app.Status == target.KnownStatus {
			reservations[app.Name] = app.PortReservation
		}
	}
	for i := range *s.Apps.Value {
		app := &(*s.Apps.Value)[i]
		if app.AllocatedHostPort.Status != target.Unknown || !app.UnitsAbsent() || reservations[app.Name] == nil {
			continue
		}
		copy := *reservations[app.Name]
		app.CommittedPortReservation = &copy
	}
}
