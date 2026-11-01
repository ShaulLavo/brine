package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func readUDPState(ctx context.Context, tx *sql.Tx) (*plan.UDPState, error) {
	state := &plan.UDPState{Incarnations: []plan.AppIncarnation{}, Reservations: []plan.UDPReservation{}}
	rows, err := tx.QueryContext(ctx, "SELECT app,incarnation_id FROM release_heads ORDER BY app")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var i plan.AppIncarnation
		if err = rows.Scan(&i.App, &i.ID); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if !digestPattern.MatchString(i.ID) {
			return nil, errors.Join(&IntegrityError{}, rows.Close())
		}
		state.Incarnations = append(state.Incarnations, i)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,app,incarnation,family,bind_address,host_port,state,COALESCE(owner,''),canonical FROM udp_reservations WHERE state<>'released' ORDER BY id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r plan.UDPReservation
		var raw []byte
		if err = rows.Scan(&r.ID, &r.App, &r.Incarnation, &r.Endpoint.Family, &r.Endpoint.BindAddress, &r.Endpoint.HostPort, &r.State, &r.Owner, &raw); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		var identity plan.UDPReservation
		if json.Unmarshal(raw, &identity) != nil || identity.ID != r.ID || identity.App != r.App || identity.Incarnation != r.Incarnation || identity.Endpoint != r.Endpoint || r.Endpoint.Validate() != nil || !digestPattern.MatchString(r.Incarnation) {
			return nil, errors.Join(&IntegrityError{}, rows.Close())
		}
		state.Reservations = append(state.Reservations, r)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return state, nil
}

func reserveUDPInventory(state *plan.UDPState, inventory *target.ControlInventory) {
	inventory.UDPReservations = make([]target.ControlUDPReservation, 0, len(state.Reservations))
	for _, r := range state.Reservations {
		inventory.UDPReservations = append(inventory.UDPReservations, target.ControlUDPReservation{ID: r.ID, App: r.App, Incarnation: r.Incarnation, State: r.State, Owner: r.Owner, Family: r.Endpoint.Family, BindAddress: r.Endpoint.BindAddress, HostPort: r.Endpoint.HostPort})
	}
}
