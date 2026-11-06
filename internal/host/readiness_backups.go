package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/store"
)

func hasReadinessRelease(releases []plan.CurrentRelease, app string) bool {
	for _, release := range releases {
		if release.App == app {
			return true
		}
	}
	return false
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
