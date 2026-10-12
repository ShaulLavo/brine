package plan

import (
	"errors"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

func canonicalManagedReservations(reservations []target.ManagedPortReservation) error {
	slices.SortFunc(reservations, func(a, b target.ManagedPortReservation) int { return strings.Compare(a.App, b.App) })
	ports := map[target.Port]bool{}
	bindings := map[string]bool{}
	for i, reservation := range reservations {
		if !reservation.Valid() || bindings[string(reservation.BindingID)] || i > 0 && reservations[i-1].App == reservation.App {
			return errors.New("invalid managed port reservation")
		}
		bindings[string(reservation.BindingID)] = true
		for _, port := range []target.Port{reservation.EnginePort, reservation.FrontendPort} {
			if port == 0 {
				continue
			}
			if ports[port] {
				return errors.New("duplicate managed port reservation")
			}
			ports[port] = true
		}
	}
	return nil
}
