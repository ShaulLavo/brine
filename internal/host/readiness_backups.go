package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/store"
)

func readinessDatabaseCoverage(ctx context.Context, snapshot *store.Store, state plan.BrineState) error {
	bindings := map[store.ReadinessBinding]struct{}{}
	for _, release := range state.Releases {
		if err := readinessReleaseBindings(ctx, snapshot, release, bindings); err != nil {
			return err
		}
	}
	return readinessBindingCoverage(ctx, snapshot, bindings)
}

func readinessReleaseBindings(ctx context.Context, snapshot *store.Store, release plan.CurrentRelease, bindings map[store.ReadinessBinding]struct{}) error {
	p, _, err := snapshot.LoadPlan(ctx, release.PlanID)
	if err != nil {
		return err
	}
	for _, mount := range p.DataMounts {
		binding := store.ReadinessBinding{App: release.App, Engine: data.EngineSQLite, SQLite: mount.Database}
		if err := readinessExpectedBinding(bindings, binding); err != nil {
			return err
		}
	}
	if p.Managed != nil {
		binding := store.ReadinessBinding{App: release.App, Engine: data.EngineSpacetimeDB, SpacetimeDB: p.Managed.Database}
		return readinessExpectedBinding(bindings, binding)
	}
	return nil
}

func readinessExpectedBinding(bindings map[store.ReadinessBinding]struct{}, binding store.ReadinessBinding) error {
	if _, duplicate := bindings[binding]; duplicate || len(bindings) == readiness.MaxItems {
		return store.ErrInvalid
	}
	bindings[binding] = struct{}{}
	return nil
}

func readinessBindingCoverage(ctx context.Context, snapshot *store.Store, bindings map[store.ReadinessBinding]struct{}) error {
	active, err := snapshot.ReadReadinessBindings(ctx, readiness.MaxItems)
	if err != nil {
		return err
	}
	for _, binding := range active {
		if _, ok := bindings[binding]; !ok {
			return store.ErrNotFound
		}
		delete(bindings, binding)
	}
	if len(bindings) != 0 {
		return store.ErrNotFound
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
