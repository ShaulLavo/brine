package store

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/target"
)

func readManagedReservations(ctx context.Context, q dataQuerier) ([]target.ManagedPortReservation, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.app,p.binding_id FROM data_engine_preparations p JOIN data_replica_bindings b ON b.id=p.binding_id JOIN data_databases d ON d.id=b.database_id JOIN data_active_incarnations a ON a.incarnation_id=d.incarnation_id ORDER BY a.app,p.binding_id`)
	if err != nil {
		return nil, err
	}
	type owner struct {
		app     string
		binding data.ReplicaBindingID
	}
	owners := []owner{}
	for rows.Next() {
		var owned owner
		if err := rows.Scan(&owned.app, &owned.binding); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		owners = append(owners, owned)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	reserved := make([]target.ManagedPortReservation, 0, len(owners))
	for _, owned := range owners {
		p, err := readManagedPreparation(ctx, q, owned.binding)
		if err != nil {
			return nil, err
		}
		item := target.ManagedPortReservation{App: owned.app, BindingID: owned.binding, EnginePort: target.Port(p.EnginePort), FrontendPort: target.Port(p.FrontendPort)}
		if !item.Valid() {
			return nil, &IntegrityError{}
		}
		reserved = append(reserved, item)
	}
	return reserved, nil
}
