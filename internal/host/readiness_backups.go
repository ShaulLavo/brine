package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/store"
)

// The existing writer read port enumerates every SQLite database, including
// undeclared preparations. An engine it cannot enumerate stays unknown.
func readinessDatabaseCoverage(ctx context.Context, snapshot *store.Store, state plan.BrineState) error {
	bindings := map[data.DatabaseID]data.SQLiteDatabaseBinding{}
	for _, release := range state.Releases {
		p, _, err := snapshot.LoadPlan(ctx, release.PlanID)
		if err != nil {
			return err
		}
		for _, mount := range p.DataMounts {
			bindings[mount.Database.DatabaseID] = mount.Database
		}
	}
	for _, incarnation := range state.DataIncarnations {
		if err := readinessIncarnationCoverage(ctx, snapshot, incarnation.ID, bindings); err != nil {
			return err
		}
	}
	if len(bindings) != 0 {
		return store.ErrNotFound
	}
	return nil
}

func readinessIncarnationCoverage(ctx context.Context, snapshot *store.Store, incarnation string, bindings map[data.DatabaseID]data.SQLiteDatabaseBinding) error {
	permits, err := snapshot.ReadWriterPermits(ctx, data.AppIncarnationID(incarnation))
	if err != nil {
		return err
	}
	if len(permits) > readiness.MaxItems {
		return store.ErrInvalid
	}
	for _, permit := range permits {
		if expected, ok := bindings[permit.Database.DatabaseID]; !ok || expected != permit.Database {
			return store.ErrNotFound
		}
		delete(bindings, permit.Database.DatabaseID)
	}
	return nil
}
func readinessReleaseBackups(ctx context.Context, dir string, snapshot *store.Store, release plan.CurrentRelease) ([]readiness.Backup, error) {
	out := []readiness.Backup{}
	if native := release.Desired.SpacetimeDB; native != nil {
		status, err := snapshot.NativeBackupStatus(ctx, release.App, string(native.Name))
		if err != nil {
			return nil, err
		}
		out = append(out, readiness.Backup{App: release.App, Database: string(native.Name), Engine: "spacetimedb", State: readiness.Known(status.State), Stale: readiness.Known(status.State != "verified" || status.MissedCaptureIntervals > 0 || status.CredentialExpired)})
	}
	for _, database := range release.Desired.Databases {
		value := readinessSQLiteBackup(ctx, dir, release.App, string(database.Name))
		out = append(out, value)
	}
	return out, nil
}
