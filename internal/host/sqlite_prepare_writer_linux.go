//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/ShaulLavo/brine/internal/quadlet"
)

func (g *sqliteHostGuard) bindWriter(ctx context.Context) error {
	release, err := g.host.state.CurrentRelease(ctx, g.scope.App)
	if err != nil {
		return errSQLitePrepareHost
	}
	for _, unit := range release.Units {
		if unit.Name == g.scope.App+".container" {
			g.writer, g.writerRelease = unit, release.ID
			return g.verifyWriterArtifacts(ctx)
		}
	}
	return errSQLitePrepareHost
}

func (g *sqliteHostGuard) verifyWriterArtifacts(ctx context.Context) error {
	release, err := g.host.state.CurrentRelease(ctx, g.scope.App)
	if err != nil || release.ID != g.writerRelease || !slices.Contains(release.Units, g.writer) {
		return errSQLitePrepareHost
	}
	native := filepath.Join(g.host.home, ".config/systemd/user", g.appUnit())
	if _, err := os.Lstat(native); !errors.Is(err, os.ErrNotExist) {
		return errSQLitePrepareHost
	}
	if quadlet.VerifyCurrent(ctx, g.host.home, g.writer.Name, g.writer.Hash) != nil || verifyManagedFrontendLoaded(ctx, g.host.session, g.host.home, g.scope.App) != nil {
		return errSQLitePrepareHost
	}
	return nil
}

func (g *sqliteHostGuard) writerOwned(ctx context.Context, writer unitState) bool {
	if writer.Load != "loaded" || g.verifyWriterArtifacts(ctx) != nil {
		return false
	}
	if writer.stopped(false) {
		return true
	}
	return writer.Active == "active" && writer.Sub == "running" && g.processInUnit(writer, g.appUnit())
}
