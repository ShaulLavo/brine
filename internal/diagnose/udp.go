package diagnose

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

type releasePlan struct {
	Plan    plan.Plan
	Desired policy.Desired
}

func (r Reader) udpState(ctx context.Context, snapshot target.Snapshot) *plan.UDPState {
	if r.Store == nil {
		return nil
	}
	generation := uint64(0)
	if snapshot.Generation.Status == target.KnownStatus && snapshot.Generation.Value != nil {
		generation = *snapshot.Generation.Value
	}
	state, err := bounded(ctx, ProbeTimeout, func(ctx context.Context) (plan.BrineState, error) {
		return r.Store.LoadBrineState(ctx, snapshot.Identity, generation)
	})
	if err != nil {
		return nil
	}
	return state.UDP
}

func retiredUDPRemnants(snapshot target.Snapshot, p plan.Plan) []plan.UDPEndpoint {
	if p.UDP == nil || p.UDP.Retire == nil || snapshot.UDPSockets.Status != target.KnownStatus || snapshot.UDPSockets.Value == nil {
		return nil
	}
	endpoint := p.UDP.Retire.Endpoint
	if p.UDP.Next != nil && p.UDP.Next.Endpoint == endpoint {
		return nil
	}
	for _, socket := range *snapshot.UDPSockets.Value {
		if endpoint.Overlaps(socket.Address, socket.Port) {
			return []plan.UDPEndpoint{endpoint}
		}
	}
	return nil
}

var udpReasonRules = []rule{
	{"udp_mapping_missing", "error", "The committed UDP mapping is missing. Inspect status and unit logs before replanning.", []string{"brine status APP --target NAME", "brine logs APP --target NAME"}, udpReason("mapping_missing")},
	{"udp_mapping_mismatch", "error", "The observed UDP mapping or unit differs from the committed allocation. Inspect it before replanning; do not edit runtime bindings directly.", []string{"brine status APP --target NAME"}, udpReason("mapping_mismatch")},
	{"udp_network_mode_unsupported", "error", "The UDP container is not using pasta. Inspect the owned unit and runtime; do not enable host networking or widen app privileges.", []string{"brine status APP --target NAME"}, udpReason("network_mode_unsupported")},
	{"udp_socket_missing", "error", "No UDP socket was observed at the committed endpoint. Inspect the app logs and publication; HTTP health does not prove UDP readiness.", []string{"brine status APP --target NAME", "brine logs APP --target NAME"}, udpReason("socket_missing")},
	{"udp_socket_foreign", "error", "An overlapping UDP socket lacks exact ownership proof. Ask the operator to inspect it; do not stop or remove an unexplained listener.", []string{"brine status APP --target NAME"}, udpReason("socket_foreign")},
	{"udp_bind_address_absent", "error", "The approved UDP bind address is not present. Ask the operator to inspect host networking; Brine does not change addresses, DNS, Tailscale or firewalls.", []string{"brine status APP --target NAME"}, udpReason("bind_address_absent")},
	{"udp_observation_unknown", "warning", "UDP readiness evidence or reservation identity is incomplete. Inspect status again; unknown evidence is not readiness or permission to retry a mutation.", []string{"brine status APP --target NAME"}, udpReason("observation_unknown")},
	{"udp_remnant", "error", "A UDP socket still overlaps a retired endpoint. Ask the operator to inspect its identity; do not delete the socket or release a reservation without absence proof.", []string{"brine status APP --target NAME"}, func(_ Host, a App) bool { return len(a.UDPRemnants) > 0 }},
	{"udp_operator_network", "info", "Remote UDP reachability is unverified. The operator owns host and provider firewalls and the public or tailnet path. Local readiness does not verify the app protocol.", []string{"brine status APP --target NAME"}, func(_ Host, a App) bool { return a.UDP != nil || len(a.UDPRemnants) > 0 }},
}

func udpReason(reason string) func(Host, App) bool {
	return func(_ Host, a App) bool { return a.UDP != nil && slices.Contains(a.UDP.LocalReasons, reason) }
}

func diagnosticUDP(snapshot target.Snapshot, state *plan.UDPState, name string, p plan.Plan, units []target.Unit) *apps.UDPStatus {
	current := plan.CurrentRelease{App: name, Units: units}
	if p.UDP != nil {
		current.UDP = p.UDP.Next
	}
	return apps.UDPStatusFor(snapshot, state, current)
}
