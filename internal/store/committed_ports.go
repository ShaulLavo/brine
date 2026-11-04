package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func reserveCommittedPorts(ctx context.Context, tx *sql.Tx, inventory *target.ControlInventory, apps map[string]target.ControlApp, own map[string]bool) error {
	rows, err := tx.QueryContext(ctx, "SELECT app,current_id FROM release_heads ORDER BY app")
	if err != nil {
		return err
	}
	type head struct{ app, id string }
	heads := []head{}
	for rows.Next() {
		var h head
		if err := rows.Scan(&h.app, &h.id); err != nil {
			return errors.Join(err, rows.Close())
		}
		heads = append(heads, h)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, h := range heads {
		if err := reserveCommittedPort(ctx, tx, inventory, apps, own, h.app, h.id); err != nil {
			return err
		}
	}
	return nil
}

func reserveCommittedPort(ctx context.Context, tx *sql.Tx, inventory *target.ControlInventory, apps map[string]target.ControlApp, own map[string]bool, app, id string) error {
	release, err := readRelease(ctx, tx, app, id)
	if err != nil {
		return err
	}
	planned, _, err := loadPlan(ctx, tx, release.PlanID)
	if err != nil {
		return err
	}
	if inventory.Target != nil && *inventory.Target != planned.Target {
		return &IntegrityError{}
	}
	inventory.Target = &planned.Target
	if release.Managed != nil || planned.Managed != nil {
		return nil
	}
	for _, reservation := range inventory.ManagedReservations {
		if reservation.App == app {
			return nil
		}
	}
	pending, err := foreignPending(ctx, tx, app, own)
	if err != nil || pending {
		return err
	}
	owned := apps[app]
	if planned.UDP != nil && planned.UDP.Next != nil {
		var incarnation string
		if err := tx.QueryRowContext(ctx, "SELECT incarnation_id FROM release_heads WHERE app=? AND current_id=?", app, id).Scan(&incarnation); err != nil {
			return err
		}
		owned.UDP = controlUDPRelease(planned, release.Units, incarnation)
	}
	owned.PortReservation = &target.CommittedPortReservation{ReleaseID: release.ID, Port: release.HostPort}
	apps[app] = owned
	return nil
}

func controlUDPRelease(p plan.Plan, units []target.Unit, incarnation string) *target.ControlUDPRelease {
	a := p.UDP.Next
	return &target.ControlUDPRelease{Incarnation: incarnation, PlanID: p.Hash, Units: units, Publication: target.UDPPublication{BindAddress: a.Endpoint.BindAddress, HostPort: a.Endpoint.HostPort, ContainerPort: a.ContainerPort, NetworkMode: "pasta"}}
}

// foreignPending reports an unfinished app or global mutation outside the
// observing operation family. Such a mutation may still move the head.
func foreignPending(ctx context.Context, tx *sql.Tx, app string, own map[string]bool) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM operations WHERE (app=? OR app='') AND state NOT IN ('succeeded','failed','rolled_back')", app)
	if err != nil {
		return false, err
	}
	pending := false
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		pending = pending || !own[id]
	}
	return pending, errors.Join(rows.Err(), rows.Close())
}
