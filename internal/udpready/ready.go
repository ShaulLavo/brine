// Package udpready evaluates local UDP evidence without sending packets or changing runtime state.
package udpready

import (
	"slices"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

type State string

const (
	Ready    State = "ready"
	NotReady State = "not_ready"
	Stopped  State = "stopped"
	Unknown  State = "unknown"
)

var reasons = []string{"mapping_missing", "mapping_mismatch", "network_mode_unsupported", "socket_missing", "socket_foreign", "bind_address_absent", "observation_unknown"}

type Result struct {
	Local   State    `json:"local"`
	Reasons []string `json:"local_reasons"`
}

func (r Result) Valid() bool {
	if r.Reasons == nil || !slices.Contains([]State{Ready, NotReady, Stopped, Unknown}, r.Local) {
		return false
	}
	seen := map[string]bool{}
	for _, reason := range r.Reasons {
		if !slices.Contains(reasons, reason) || seen[reason] {
			return false
		}
		seen[reason] = true
	}
	switch r.Local {
	case Ready, Stopped:
		return len(r.Reasons) == 0
	case Unknown:
		return len(r.Reasons) == 1 && r.Reasons[0] == "observation_unknown"
	default:
		return len(r.Reasons) > 0 && (len(r.Reasons) > 1 || r.Reasons[0] != "observation_unknown")
	}
}

// Check consumes collector-proved publication and socket ownership. Unit hashes
// bind that proof to the caller's expected release or operation, not just a name.
func Check(s target.Snapshot, app string, allocation plan.UDPAllocation, units []target.Unit) Result {
	a := observedApp(s, app)
	if a == nil {
		return Result{Unknown, []string{"observation_unknown"}}
	}
	found := []string{}
	unitKnown := a.QuadletUnits.Status == target.KnownStatus && a.QuadletUnits.Value != nil
	if !unitKnown {
		found = append(found, "observation_unknown")
	} else if len(units) == 0 || !sameUnits(units, *a.QuadletUnits.Value) {
		found = append(found, "mapping_mismatch")
	}
	socketReasons := checkSockets(s.UDPSockets, app, allocation.Endpoint)
	inactive := a.UnitActive != nil && a.UnitActive.Status == target.KnownStatus && a.UnitActive.Value != nil && !*a.UnitActive.Value
	if inactive && len(found) == 0 && slices.Equal(socketReasons, []string{"socket_missing"}) && a.UDPPublication.Status == target.Absent {
		return Result{Stopped, []string{}}
	}
	found = append(found, checkMapping(a.UDPPublication, allocation)...)
	found = append(found, socketReasons...)
	if s.LocalAddresses.Status != target.KnownStatus || s.LocalAddresses.Value == nil {
		found = append(found, "observation_unknown")
	} else if !slices.Contains(*s.LocalAddresses.Value, allocation.Endpoint.BindAddress) {
		found = append(found, "bind_address_absent")
	}
	if a.UnitActive == nil || a.UnitActive.Status != target.KnownStatus || a.UnitActive.Value == nil {
		found = append(found, "observation_unknown")
	} else if inactive {
		found = append(found, "mapping_missing")
	}
	slices.Sort(found)
	found = slices.Compact(found)
	if len(found) == 0 {
		return Result{Ready, found}
	}
	if slices.Equal(found, []string{"observation_unknown"}) {
		return Result{Unknown, found}
	}
	return Result{NotReady, found}
}

func observedApp(s target.Snapshot, name string) *target.App {
	if s.Apps.Status != target.KnownStatus || s.Apps.Value == nil {
		return nil
	}
	for i := range *s.Apps.Value {
		if (*s.Apps.Value)[i].Name == name {
			return &(*s.Apps.Value)[i]
		}
	}
	return nil
}

func sameUnits(expected, live []target.Unit) bool {
	if len(expected) != len(live) {
		return false
	}
	for _, unit := range expected {
		if !slices.Contains(live, unit) {
			return false
		}
	}
	return true
}

func checkMapping(o target.Observation[target.UDPPublication], a plan.UDPAllocation) []string {
	if o.Status == target.Absent {
		return []string{"mapping_missing"}
	}
	if o.Status != target.KnownStatus || o.Value == nil {
		return []string{"observation_unknown"}
	}
	out := []string{}
	if o.Value.BindAddress != a.Endpoint.BindAddress || o.Value.HostPort != a.Endpoint.HostPort || o.Value.ContainerPort != a.ContainerPort {
		out = append(out, "mapping_mismatch")
	}
	if o.Value.NetworkMode != "pasta" {
		out = append(out, "network_mode_unsupported")
	}
	return out
}

func checkSockets(o target.Observation[[]target.UDPSocket], app string, e plan.UDPEndpoint) []string {
	if o.Status != target.KnownStatus || o.Value == nil {
		return []string{"observation_unknown"}
	}
	count := 0
	foreign := false
	for _, socket := range *o.Value {
		if !e.Overlaps(socket.Address, socket.Port) {
			continue
		}
		count++
		foreign = foreign || socket.Address != e.BindAddress || socket.App != app || socket.Process == "" || socket.Unit != app+".service" && socket.Unit != "brine-"+app+".service"
	}
	if count == 0 {
		return []string{"socket_missing"}
	}
	if foreign || count != 1 {
		return []string{"socket_foreign"}
	}
	return []string{}
}
