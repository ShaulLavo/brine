//go:build linux

package store

import "testing"

func TestPriorSchemaFixtureRemovesLaterRemovalAndDropObjects(t *testing.T) {
	s := openTest(t)
	dropSQLitePreparationMigrationFixture(t, t.Context(), s)
	var count int
	err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE
 name GLOB 'data_sqlite_archive_*' OR name GLOB 'data_sqlite_retire_*' OR
 name GLOB 'data_sqlite_prep*' OR name GLOB 'drop_*'`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("old-schema fixture retained later objects", count, err)
	}
	if _, err = s.db.ExecContext(t.Context(), "UPDATE schema_version SET version=34"); err != nil {
		t.Fatal(err)
	}
	if err = s.migrate(t.Context()); err != nil {
		t.Fatal("genuine predecessor cannot migrate forward", err)
	}
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_foreign_key_check").Scan(&count); err != nil || count != 0 {
		t.Fatal("rewind or migration broke foreign keys", count, err)
	}
}
