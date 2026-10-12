//go:build linux

package host

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
)

func (h archiveHost) Cover(ctx context.Context, p archive.Plan, database archive.Database, operation string, writersAt time.Time) (archive.Coverage, error) {
	if native, ok := database.Native(); ok {
		return h.coverNativeArchive(ctx, p, native, operation, writersAt)
	}
	concrete, sqlite := database.SQLite()
	if !sqlite {
		return archive.Coverage{}, archive.ErrRefused
	}
	covered, err := h.coverSQLite(ctx, p, concrete, operation, writersAt)
	return archive.SQLiteCoverageVariant(covered), err
}
