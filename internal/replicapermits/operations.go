package replicapermits

import (
	"context"

	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type OperationUnits interface {
	Show(context.Context, systemd.Unit) (systemd.Properties, error)
}
type Operations struct{ Units OperationUnits }

func (o Operations) OperationActive(ctx context.Context, operation string) (bool, error) {
	id, err := systemd.ParseOperationID(operation)
	if err != nil || o.Units == nil || ctx.Err() != nil {
		return false, replication.ErrPermit
	}
	unit, err := systemd.ParseUnit("brine-op-" + id.String() + ".service")
	if err != nil {
		return false, replication.ErrPermit
	}
	properties, err := o.Units.Show(ctx, unit)
	if err != nil || ctx.Err() != nil {
		return false, replication.ErrPermit
	}
	return properties.ActiveState == "active" && properties.SubState == "running", nil
}
