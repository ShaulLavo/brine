//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/result"
)

func readinessPolicy() readiness.PolicyLoader { return nil }
func readinessSQLiteBackup(_ context.Context, _ string, app, database string) readiness.Backup {
	return readiness.Backup{App: app, Database: database, Engine: "sqlite", State: readiness.Unknown[string]("probe_unavailable"), Stale: readiness.Unknown[bool]("probe_unavailable")}
}

func newReadinessInventory(context.Context) (dispatch.Inventory, error) {
	return nil, result.New(result.DependencyMissing, nil)
}
