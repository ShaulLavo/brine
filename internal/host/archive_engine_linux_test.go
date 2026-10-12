//go:build linux

package host

import (
	"github.com/ShaulLavo/brine/internal/archive"
	"testing"
)

func sqliteArchiveDatabase(t testing.TB, value archive.Database) archive.SQLiteDatabase {
	t.Helper()
	record, ok := value.SQLite()
	if !ok {
		t.Fatal("expected SQLite archive arm")
	}
	return record
}
