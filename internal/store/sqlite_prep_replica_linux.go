//go:build linux

package store

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

// requireRunningReplica proves a running-mode claim: the replica supervisor
// holds its lifetime lock. Acquiring it would show the replica is stopped.
func (g *SQLiteMutationGuard) requireRunningReplica(ctx context.Context) error {
	lock, err := replication.AcquireLifetimeLock(ctx, filepath.Join(g.store.dir, "replica-locks", string(g.binding)+".lock"))
	if errors.Is(err, replication.ErrLocked) {
		return nil
	}
	if err == nil {
		err = lock.Release()
	}
	return errors.Join(sqliteRefuse(sqliteprep.LocalUncertain), err)
}
