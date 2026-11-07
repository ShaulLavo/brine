//go:build linux

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestControlCaptureInspectAnySchema(t *testing.T) {
	for _, version := range []int{0, 1, SchemaVersion, SchemaVersion + 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) { testInspectControlVersion(t, version) })
	}
}

func testInspectControlVersion(t *testing.T, version int) {
	t.Helper()
	dir := controlSchemaFixture(t, fmt.Sprintf("INSERT INTO schema_version VALUES(%d)", version), "")
	c := captureControlFixture(t, dir)
	before := captureTree(t, dir)
	scratch := t.TempDir()
	f, err := InspectCapture(context.Background(), c, scratch)
	if err != nil || f.Version != version || !f.Integrity || !f.ForeignKeys || f.Absent {
		t.Fatalf("schema %d inspection: %+v %v", version, f, err)
	}
	assertCaptureTree(t, dir, before)
	assertCaptureScratchEmpty(t, scratch)
	if version <= SchemaVersion {
		return
	}
	data, _, err := RehearseMigration(context.Background(), c, scratch)
	var schemaError *SchemaError
	if !errors.As(err, &schemaError) || schemaError.Version != version || data != nil {
		t.Fatalf("newer migration accepted: %v", err)
	}
	assertCaptureScratchEmpty(t, scratch)
}

func TestControlCaptureRehearsesRealOldMigration(t *testing.T) {
	dir, fingerprint := v34Store(t, "")
	c := captureControlFixture(t, dir)
	before := captureTree(t, dir)
	scratch := t.TempDir()
	old, err := InspectCapture(context.Background(), c, scratch)
	if err != nil || old.Version != 34 {
		t.Fatalf("old schema: %+v %v", old, err)
	}
	data, f, err := RehearseMigration(context.Background(), c, scratch)
	if err != nil || f.Version != SchemaVersion || !f.Integrity || !f.ForeignKeys {
		t.Fatalf("migration: %+v %v", f, err)
	}
	normalized := stateDir(t)
	writeCaptureFixture(t, filepath.Join(normalized, "control.db"), data)
	read, err := OpenReadOnly(context.Background(), normalized)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	if after := journalFingerprint(t, read.db); after != fingerprint {
		t.Fatal("migration changed historical rows")
	}
	var count int
	if err := read.db.QueryRow("SELECT count(*) FROM data_sqlite_preparations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("actual version 35 migration missing: %d %v", count, err)
	}
	assertCaptureTree(t, dir, before)
	assertCaptureScratchEmpty(t, scratch)
}

func TestControlCaptureSchemaRefusalsAndCleanup(t *testing.T) {
	for _, name := range []string{"missing_version", "empty_version", "duplicate_version", "negative_version", "text_version", "foreign_key", "integrity", "migration_failure", "corrupt"} {
		t.Run(name, func(t *testing.T) { testControlSchemaRefusal(t, name) })
	}
}

func testControlSchemaRefusal(t *testing.T, name string) {
	t.Helper()
	version := fmt.Sprintf("INSERT INTO schema_version VALUES(%d)", SchemaVersion)
	extra := ""
	switch name {
	case "missing_version":
		extra = "DROP TABLE schema_version"
	case "empty_version":
		version = ""
	case "duplicate_version":
		version = "INSERT INTO schema_version VALUES(1),(1)"
	case "negative_version":
		version = "INSERT INTO schema_version VALUES(-1)"
	case "text_version":
		version = "INSERT INTO schema_version VALUES('invalid')"
	case "foreign_key":
		extra = "CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER REFERENCES parent(id)); INSERT INTO child VALUES(99)"
	case "integrity":
		extra = "CREATE TABLE invalid(value INTEGER); INSERT INTO invalid VALUES(NULL); PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql='CREATE TABLE invalid(value INTEGER NOT NULL)' WHERE name='invalid'"
	case "migration_failure":
		version = "INSERT INTO schema_version VALUES(1)"
	}
	dir := controlSchemaFixture(t, version, extra)
	if name == "corrupt" {
		writeCaptureFixture(t, filepath.Join(dir, "control.db"), []byte("not SQLite"))
	}
	c := captureControlFixture(t, dir)
	before := captureTree(t, dir)
	scratch := t.TempDir()

	facts, err := InspectCapture(context.Background(), c, scratch)
	assertControlSchemaRefusal(t, name, facts, err)
	data, _, err := RehearseMigration(context.Background(), c, scratch)
	if err == nil || data != nil {
		t.Fatalf("invalid migration accepted: %v", err)
	}
	assertCaptureTree(t, dir, before)
	assertCaptureScratchEmpty(t, scratch)
}

func assertControlSchemaRefusal(t *testing.T, name string, facts SchemaFacts, err error) {
	t.Helper()
	expected, valid := map[string]SchemaFacts{
		"foreign_key":       {Version: SchemaVersion, Integrity: true},
		"integrity":         {Version: SchemaVersion, ForeignKeys: true},
		"migration_failure": {Version: 1, Integrity: true, ForeignKeys: true},
	}[name]
	if !valid {
		if err == nil {
			t.Fatalf("malformed schema accepted: %+v", facts)
		}
		return
	}
	if err != nil || facts != expected {
		t.Fatalf("%s facts: %+v %v", name, facts, err)
	}
}

func TestControlCaptureCopyCancellationAndCleanup(t *testing.T) {
	dir := controlSchemaFixture(t, "INSERT INTO schema_version VALUES(35)", "")
	c := captureControlFixture(t, dir)
	before := captureTree(t, dir)
	for _, migrate := range []bool{false, true} {
		testControlCopyCancellation(t, c, migrate, 1)
		testControlCopyCancellation(t, c, migrate, 4)
		testControlSQLCancellation(t, c, migrate)
	}
	assertCaptureTree(t, dir, before)
}

func testControlCopyCancellation(t *testing.T, c Capture, migrate bool, boundary int) {
	t.Helper()
	scratch := t.TempDir()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &captureBoundaryContext{Context: parent, at: boundary, action: cancel}
	var err error
	if migrate {
		_, _, err = RehearseMigration(ctx, c, scratch)
	} else {
		_, err = InspectCapture(ctx, c, scratch)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy cancellation, migration %t boundary %d: %v", migrate, boundary, err)
	}
	assertCaptureScratchEmpty(t, scratch)
}

type captureSQLContext struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *captureSQLContext) Done() <-chan struct{} {
	c.once.Do(c.cancel)
	return c.Context.Done()
}

func testControlSQLCancellation(t *testing.T, c Capture, migrate bool) {
	t.Helper()
	scratch := t.TempDir()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &captureSQLContext{Context: parent, cancel: cancel}
	var err error
	if migrate {
		_, _, err = RehearseMigration(ctx, c, scratch)
	} else {
		_, err = InspectCapture(ctx, c, scratch)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SQLite cancellation, migration %t: %v", migrate, err)
	}
	assertCaptureScratchEmpty(t, scratch)
}

func TestControlCaptureRejectsAlteredManifest(t *testing.T) {
	dir := controlSchemaFixture(t, "INSERT INTO schema_version VALUES(35)", "")
	c := captureControlFixture(t, dir)
	c.DB[0] ^= 1
	scratch := t.TempDir()
	if _, err := InspectCapture(context.Background(), c, scratch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed bytes: %v", err)
	}
	c = Capture{Absent: true, WAL: []byte{}}
	if _, _, err := RehearseMigration(context.Background(), c, scratch); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ambiguous absence: %v", err)
	}
	assertCaptureScratchEmpty(t, scratch)
}

func controlSchemaFixture(t *testing.T, version, extra string) string {
	t.Helper()
	dir := stateDir(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("CREATE TABLE schema_version(version INTEGER NOT NULL);" + version + ";" + extra); err != nil {
		t.Fatal(err)
	}
	requireCaptureFixture(t, db.Close())
	requireCaptureFixture(t, os.Chmod(filepath.Join(dir, "control.db"), 0600))
	return dir
}

func captureControlFixture(t *testing.T, dir string) Capture {
	t.Helper()
	c, err := CaptureBytes(context.Background(), dir, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestControlCaptureRawInspectionIsReadOnly(t *testing.T) {
	dir := controlSchemaFixture(t, "INSERT INTO schema_version VALUES(1)", "")
	db, err := openControlCapture(context.Background(), filepath.Join(dir, "control.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("UPDATE schema_version SET version=2"); err == nil {
		t.Fatal("raw inspection permitted a write")
	}
	f, err := inspectControlSchema(context.Background(), db)
	if err != nil || f.Version != 1 {
		t.Fatalf("raw schema changed: %+v %v", f, err)
	}
}

func TestControlCaptureRehearsesVersionZero(t *testing.T) {
	dir := controlSchemaFixture(t, "INSERT INTO schema_version VALUES(0)", "")
	c := captureControlFixture(t, dir)
	before := captureTree(t, dir)
	scratch := t.TempDir()
	data, f, err := RehearseMigration(context.Background(), c, scratch)
	if err != nil || len(data) == 0 || len(data) > maxPreviewBytes || f.Version != SchemaVersion || !f.Integrity || !f.ForeignKeys {
		t.Fatalf("full migration: %+v %v", f, err)
	}
	assertCaptureTree(t, dir, before)
	assertCaptureScratchEmpty(t, scratch)
}

func TestControlCaptureScratchIsPrivateAndOwned(t *testing.T) {
	dir := controlSchemaFixture(t, "INSERT INTO schema_version VALUES(35)", "")
	c := captureControlFixture(t, dir)
	scratch := t.TempDir()
	staged, err := stageControlCapture(context.Background(), c, scratch)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(staged) }()
	info, err := os.Stat(staged)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("scratch permissions: %v %v", info, err)
	}
	requireCaptureFixture(t, readOnlyOwner(staged))
	file, err := os.Stat(filepath.Join(staged, "control.db"))
	if err != nil || file.Mode().Perm() != 0600 {
		t.Fatalf("copy permissions: %v %v", file, err)
	}
	requireCaptureFixture(t, readOnlyOwner(filepath.Join(staged, "control.db")))
	if _, err := os.Stat(filepath.Join(staged, "control.db-shm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("authoritative SHM copied: %v", err)
	}
	requireCaptureFixture(t, os.RemoveAll(staged))
	assertCaptureScratchEmpty(t, scratch)
}
