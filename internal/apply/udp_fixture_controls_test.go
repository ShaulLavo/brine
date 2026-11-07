//go:build linux

package apply

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/store"
)

func openUDPFixtureSQL(t *testing.T, directory string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(directory, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func udpFixtureSchema(t *testing.T, db *sql.DB) string {
	t.Helper()
	var schema string
	err := db.QueryRow(`SELECT group_concat(entry, char(10)) FROM
		(SELECT type || ':' || name || ':' || tbl_name || ':' || COALESCE(sql, '') AS entry
		 FROM sqlite_schema ORDER BY type, name)`).Scan(&schema)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestUDPTestSchemaMatchesFreshInitialization(t *testing.T) {
	directory := t.TempDir()
	if err := seedSQLiteUDP(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	seeded := openUDPFixtureSQL(t, directory)
	var version int
	if err := seeded.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != store.SchemaVersion {
		t.Fatal("seed lacks current initialization", version, err)
	}
	var integrity string
	if err := seeded.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("seed integrity", integrity, err)
	}
	freshDirectory := t.TempDir()
	fresh, err := store.OpenContext(t.Context(), freshDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if udpFixtureSchema(t, seeded) != udpFixtureSchema(t, openUDPFixtureSQL(t, freshDirectory)) {
		t.Fatal("seed differs from the complete current migration schema")
	}
	info, err := os.Stat(filepath.Join(directory, "control.db"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("seed file permissions", info, err)
	}
}

func TestUDPTestSchemaKeepsPrivateStateAndRefusesOverwrite(t *testing.T) {
	seed, err := pristineUDPTestSchema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	firstDirectory, secondDirectory := t.TempDir(), t.TempDir()
	if err := seedSQLiteUDP(t.Context(), firstDirectory); err != nil {
		t.Fatal(err)
	}
	first := newSQLiteUDPRig(t, firstDirectory, "create")
	if err := seedSQLiteUDP(t.Context(), secondDirectory); err != nil {
		t.Fatal(err)
	}
	second := newSQLiteUDPRig(t, secondDirectory, "create")
	if _, err := first.database.GetOperation(t.Context(), second.operation); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("first fixture inherited second operation", err)
	}
	if _, err := second.database.GetOperation(t.Context(), first.operation); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("second fixture inherited first operation", err)
	}
	if err := seedSQLiteUDP(t.Context(), firstDirectory); !errors.Is(err, os.ErrExist) {
		t.Fatal("seed could overwrite a recovery database", err)
	}
	if _, err := first.database.GetOperation(t.Context(), first.operation); err != nil {
		t.Fatal("refused overwrite changed the journal", err)
	}
	current, err := pristineUDPTestSchema(t.Context())
	if err != nil || current != seed {
		t.Fatal("fixture writes changed the seed", err)
	}
}

func TestUDPTestSchemaTeardown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if _, err := newUDPTestSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(root, "missing"))
	if _, err := newUDPTestSchema(t.Context()); err == nil {
		t.Fatal("seed initialization ignored its unavailable temporary directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("seed initialization retained scratch", entries, err)
	}
}
