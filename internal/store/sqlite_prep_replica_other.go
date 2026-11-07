//go:build !linux

package store

import (
	"context"

	"github.com/ShaulLavo/brine/internal/sqliteprep"
)

func (*SQLiteMutationGuard) requireRunningReplica(context.Context) error {
	return sqliteRefuse(sqliteprep.LocalUncertain)
}
