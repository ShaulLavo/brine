//go:build !linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/sqlitestatus"
)

type unavailableSQLiteStatus struct{}

func newSQLiteBackupStatusReader(string) dispatch.SQLiteBackupStatusReader {
	return unavailableSQLiteStatus{}
}
func (unavailableSQLiteStatus) SQLiteBackupStatus(context.Context, dispatch.SQLiteBackupStatusArgs) (sqlitestatus.Status, error) {
	return sqlitestatus.Status{}, result.New(result.DependencyMissing, nil)
}
