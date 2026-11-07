//go:build linux

package enroll

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/admission"
	"github.com/ShaulLavo/brine/internal/store"
)

func walUpdateCapture(t *testing.T, f *updateFixture) store.Capture {
	t.Helper()
	path := f.h.updateEnv.fs.path("/" + admissionState + "/control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, query := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE schema_version(version INTEGER)", "INSERT INTO schema_version VALUES(1)", "CREATE TABLE ledger(value TEXT)", "PRAGMA wal_checkpoint(TRUNCATE)", "INSERT INTO ledger VALUES('committed only in WAL')"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range rawNames {
		if err := os.Chmod(filepath.Join(filepath.Dir(path), name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	capture, err := store.CaptureBytes(context.Background(), filepath.Dir(path), os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if len(capture.WAL) == 0 {
		t.Fatal("fixture did not retain a WAL")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return capture
}
func prepareRawUpdate(t *testing.T, absent bool) (*updateFixture, store.Capture) {
	t.Helper()
	f := newUpdateFixture(t)
	capture := store.Capture{Absent: true}
	if !absent {
		capture = walUpdateCapture(t, f)
	}
	plan := UpdatePlan{CandidateSHA256: hash(f.h.source), RecordedBinarySHA256: hash([]byte("previous")), SchemaFrom: 1, SchemaTo: store.SchemaVersion, Quiescence: admission.Store, ControlAbsent: absent}
	if absent {
		plan.SchemaFrom = 0
	}
	f.pending(t, plan)
	if err := f.h.stageUpdateArtifacts(); err != nil {
		t.Fatal(err)
	}
	f.h.r.Update.Aborting = true
	f.h.r.Update.Capture = &updateCapture{Absent: capture.Absent, WALPresent: capture.WAL != nil, DB: capture.DBID, WAL: capture.WALID, Digest: captureDigest(capture)}
	for _, name := range rawNames {
		if err := os.WriteFile(f.h.updateEnv.fs.path("/"+admissionState+"/"+name), []byte("candidate-created "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.h.updateSave(); err != nil {
		t.Fatal(err)
	}
	return f, capture
}
func TestUpdateRawRollbackWALAndAbsence(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprint(absent), func(t *testing.T) { testRawUpdate(t, absent) })
	}
}
func testRawUpdate(t *testing.T, absent bool) {
	t.Helper()
	f, capture := prepareRawUpdate(t, absent)
	state := f.h.updateEnv.fs.path("/" + admissionState)
	for _, name := range []string{"control.db.bak", "control.db-journal"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte("unrelated"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.h.restoreUpdateCapture(context.Background(), capture); err != nil {
		t.Fatal(err)
	}
	if err := f.h.restoreUpdateCapture(context.Background(), capture); err != nil {
		t.Fatal("second rollback", err)
	}
	got, err := store.CaptureBytes(context.Background(), state, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if got.Absent != absent || !bytes.Equal(got.DB, capture.DB) || !bytes.Equal(got.WAL, capture.WAL) {
		t.Fatal("raw rollback lost capture bytes")
	}
	for _, name := range []string{"control.db.bak", "control.db-journal"} {
		data, err := os.ReadFile(filepath.Join(state, name)) // #nosec G304 -- Fixed unrelated names inside the private test fixture.
		if err != nil || string(data) != "unrelated" {
			t.Fatal("unrelated file changed", err)
		}
	}
	if absent {
		return
	}
	copy := t.TempDir()
	for name, data := range map[string][]byte{"control.db": got.DB, "control.db-wal": got.WAL} {
		if err := os.WriteFile(filepath.Join(copy, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(copy, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var value string
	if err := db.QueryRow("SELECT value FROM ledger").Scan(&value); err != nil || value != "committed only in WAL" {
		t.Fatal("committed WAL row lost", value, err)
	}
}
func TestUpdateRawRollbackEverySaveCrash(t *testing.T) {
	for at := 1; at <= 13; at++ {
		t.Run(fmt.Sprint(at), func(t *testing.T) { rawUpdateCrash(t, at) })
	}
}
func rawUpdateCrash(t *testing.T, at int) {
	t.Helper()
	f, capture := prepareRawUpdate(t, false)
	f.failSave = f.saves + at
	err := f.h.restoreUpdateCapture(context.Background(), capture)
	if err == nil {
		return
	}
	if err.Error() != "fixture crash before save" {
		t.Fatal(err)
	}
	f.restart(t)
	if err := f.h.restoreUpdateCapture(context.Background(), capture); err != nil {
		t.Fatal("did not converge after crash", err)
	}
}
func TestUpdateRawRollbackRefusesInputAndIdentityDrift(t *testing.T) {
	f, c := prepareRawUpdate(t, false)
	corrupt := c
	corrupt.DBID.SHA256 = "wrong"
	if err := f.h.restoreUpdateCapture(context.Background(), corrupt); err == nil {
		t.Fatal("different input accepted")
	}
	f.failSave = f.saves + 4
	if err := f.h.restoreUpdateCapture(context.Background(), c); err == nil {
		t.Fatal("crash control did not fire")
	}
	f.restart(t)
	dir := f.h.updateEnv.fs.path("/" + admissionState)
	if err := os.WriteFile(filepath.Join(dir, "control.db-shm"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.h.restoreUpdateCapture(context.Background(), c); err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("quarantine drift accepted")
	}
}

func TestUpdateNewerSchemaRefusesBeforeEffects(t *testing.T) {
	f := newUpdateFixture(t)
	path := f.h.updateEnv.fs.path("/" + admissionState + "/control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE schema_version(version INTEGER); INSERT INTO schema_version VALUES(?)", store.SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	before, _, err := f.h.updateEnv.fs.read("/" + admissionState + "/control.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.h.inspectUpdateStore(context.Background()); err == nil || err.Error() != "schema_newer_than_candidate" {
		t.Fatal("newer schema accepted", err)
	}
	after, _, err := f.h.updateEnv.fs.read("/" + admissionState + "/control.db")
	if err != nil || !updateSame(before, after) || f.h.r.Update != nil || len(f.launches) != 0 {
		t.Fatal("schema refusal changed live state", err)
	}
}
