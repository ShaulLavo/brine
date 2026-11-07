//go:build linux

package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func fixtureSchema(t *testing.T, db *sql.DB) [][4]string {
	t.Helper()
	rows, err := db.Query("SELECT type,name,tbl_name,COALESCE(sql,'') FROM sqlite_schema ORDER BY type,name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var schema [][4]string
	for rows.Next() {
		var row [4]string
		if err := rows.Scan(&row[0], &row[1], &row[2], &row[3]); err != nil {
			t.Fatal(err)
		}
		schema = append(schema, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestPristineTestControlDBInitialized(t *testing.T) {
	data, err := pristineTestControlDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir(t), "control.db")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	var version int
	if err := db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil || version != SchemaVersion {
		t.Fatal("seed lacks current initialization", version, err)
	}
	fresh, err := OpenContext(t.Context(), stateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fresh.Close(); err != nil {
			t.Error(err)
		}
	})
	if !reflect.DeepEqual(fixtureSchema(t, db), fixtureSchema(t, fresh.db)) {
		t.Fatal("seed differs from the complete current migration schema")
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("seed integrity", integrity, err)
	}
}

func TestPristineTestControlDBSingleImmutableSeed(t *testing.T) {
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 4)
	for range cap(results) {
		go func() {
			data, err := pristineTestControlDB(t.Context())
			results <- result{data, err}
		}()
	}
	var seed []byte
	for range cap(results) {
		got := <-results
		if got.err != nil || len(got.data) == 0 {
			t.Fatal("seed initialization", got.err)
		}
		if seed != nil && &seed[0] != &got.data[0] {
			t.Fatal("fixture initialized more than one seed")
		}
		seed = got.data
	}
	before := bytes.Clone(seed)
	first := openTest(t)
	operation(t, first)
	if !bytes.Equal(before, seed) {
		t.Fatal("fixture writes changed the seed")
	}
}

func requireEmptyFixture(t *testing.T, s *Store) {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM operations").Scan(&count); err != nil || count != 0 {
		t.Fatal("fixture inherited an operation", count, err)
	}
	for name, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if err := s.db.QueryRow("PRAGMA " + name).Scan(&got); err != nil || got != want {
			t.Fatal("fixture pragma", name, got, err)
		}
	}
	info, err := os.Stat(filepath.Join(s.dir, "control.db"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("fixture file permissions", info, err)
	}
}

func TestOpenTestPristineFixtureIsolation(t *testing.T) {
	seed, err := pristineTestControlDB(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(seed)
	first := openTest(t)
	requireEmptyFixture(t, first)
	operation(t, first)
	second := openTest(t)
	requireEmptyFixture(t, second)
	if first.db == second.db || first.dir == second.dir {
		t.Fatal("fixtures share a connection or state directory")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	operation(t, second)
	requireEmptyFixture(t, openTest(t))
	if sha256.Sum256(seed) != digest {
		t.Fatal("fixture writes changed the seed")
	}
}

func TestPristineTestControlDBTeardown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	if _, err := newPristineTestControlDB(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(root, "missing"))
	if _, err := newPristineTestControlDB(t.Context()); err == nil {
		t.Fatal("seed initialization ignored its unavailable temporary directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("seed initialization retained scratch", entries, err)
	}
}
