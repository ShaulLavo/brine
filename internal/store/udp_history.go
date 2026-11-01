package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

const maxUDPHistory = 64

type UDPHistory struct {
	Endpoints []plan.UDPEndpoint
	Complete  bool
}

// UDPHistory reads released identities for diagnosis only. Its bounded result
// never participates in reservation admission or grants runtime authority.
func (s *Store) UDPHistory(ctx context.Context, app string) (UDPHistory, error) {
	return readUDPHistory(ctx, s.db, app)
}

type udpHistoryQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readUDPHistory(ctx context.Context, q udpHistoryQuery, app string) (UDPHistory, error) {
	result := UDPHistory{Endpoints: []plan.UDPEndpoint{}, Complete: true}
	if !validAppName(app) {
		return UDPHistory{}, ErrInvalid
	}
	rows, err := q.QueryContext(ctx, `SELECT id,incarnation,family,bind_address,host_port,canonical FROM udp_reservations WHERE app=? AND state='released' ORDER BY rowid DESC LIMIT ?`, app, maxUDPHistory+1)
	if err != nil {
		return UDPHistory{}, err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	seen := map[plan.UDPEndpoint]bool{}
	for rows.Next() {
		var id, incarnation string
		var endpoint plan.UDPEndpoint
		var raw []byte
		if err := rows.Scan(&id, &incarnation, &endpoint.Family, &endpoint.BindAddress, &endpoint.HostPort, &raw); err != nil {
			return UDPHistory{}, err
		}
		count++
		if count > maxUDPHistory {
			result.Complete = false
			break
		}
		var identity plan.UDPReservation
		if len(raw) > 4096 || json.Unmarshal(raw, &identity) != nil || identity.ID != id || identity.App != app || identity.Incarnation != incarnation || identity.Endpoint != endpoint || endpoint.Validate() != nil || !digestPattern.MatchString(incarnation) {
			return UDPHistory{}, &IntegrityError{}
		}
		if seen[endpoint] {
			continue
		}
		seen[endpoint] = true
		result.Endpoints = append(result.Endpoints, endpoint)
	}
	return result, errors.Join(rows.Err(), rows.Close())
}

func retainedUDPInventory(ctx context.Context, tx *sql.Tx, app *target.ControlApp) error {
	history, err := readUDPHistory(ctx, tx, app.Name)
	if err != nil {
		return err
	}
	observation := target.Observation[[]target.ControlUDPEndpoint]{Status: target.Unknown}
	if history.Complete {
		endpoints := make([]target.ControlUDPEndpoint, 0, len(history.Endpoints))
		for _, e := range history.Endpoints {
			endpoints = append(endpoints, target.ControlUDPEndpoint{Family: e.Family, BindAddress: e.BindAddress, HostPort: e.HostPort})
		}
		observation = target.Known(endpoints)
	}
	app.RetiredUDP = &observation
	return nil
}
