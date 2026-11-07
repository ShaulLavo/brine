//go:build linux

package host

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/readiness"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestReadinessBackupsNativeCoverageIsNotBackupSuccess(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	dir := readinessNativeDirectory(t, r)
	before := diagnosticFiles(t, dir)
	snapshot, state := readinessCoverageSnapshot(t, dir, r.snapshot.Identity)
	if err := readinessDatabaseCoverage(t.Context(), snapshot, state); err != nil {
		t.Fatal("exact native release binding lost coverage", err)
	}
	backups, err := (readinessControl{dir: dir}).Backups(t.Context(), r.snapshot.Identity)
	if err != nil || len(backups) != 1 {
		t.Fatal("native backup reader was not reached", backups, err)
	}
	backup := backups[0]
	if backup.Engine != "spacetimedb" || backup.App != "hello" || backup.Database != "main" || backup.State.Value == nil || *backup.State.Value != "inactive" || backup.Stale.Value == nil || !*backup.Stale.Value {
		t.Fatal("fixture ownership implied a verified backup", backup)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("native readiness changed live files")
	}
}

func TestReadinessBackupsSQLiteCurrentReleaseCoverage(t *testing.T) {
	r := newSQLitePrepRig(t, []string{"v1"}, "v1")
	release, err := r.state.CurrentRelease(t.Context(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	planned, _, err := r.state.LoadPlan(t.Context(), release.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	before := diagnosticFiles(t, r.root)
	snapshot, state := readinessCoverageSnapshot(t, r.root, planned.Target)
	if err := readinessDatabaseCoverage(t.Context(), snapshot, state); err != nil {
		t.Fatal("exact SQLite current-release binding lost coverage", err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, r.root)) {
		t.Fatal("SQLite coverage changed live files")
	}
}

func TestReadinessBackupsRejectsExtraNativeAndMixedPreparations(t *testing.T) {
	for _, kind := range []string{"sqlite_same_app", "native_same_app", "native_other_app"} {
		t.Run(kind, func(t *testing.T) { testReadinessExtraPreparation(t, kind) })
	}
}

func testReadinessExtraPreparation(t *testing.T, kind string) {
	t.Helper()
	r := newNativeWithdrawalRig(t)
	if kind == "sqlite_same_app" {
		if _, err := r.state.ReserveDatabase(t.Context(), readinessReviewReservation("audit", "/audit")); err != nil {
			t.Fatal(err)
		}
	} else {
		app, incarnation := "hello", r.original.Managed.Database.IncarnationID
		if kind == "native_other_app" {
			app, incarnation = "other", data.AppIncarnationID(strings.Repeat("1", 32))
		}
		request := readinessNativeReservation(t, app, incarnation)
		if _, err := r.state.ReserveEngine(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	assertReadinessBackupsUnknown(t, readinessNativeDirectory(t, r), r.snapshot.Identity)
}

func TestReadinessBackupsPreparedNativeBesideStatelessRelease(t *testing.T) {
	dir, state, observed := reviewStatelessStore(t)
	t.Cleanup(func() { _ = state.Close() })
	request := readinessNativeReservation(t, "hello", data.AppIncarnationID(strings.Repeat("1", 32)))
	if _, err := state.ReserveEngine(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertReadinessBackupsUnknown(t, dir, observed.Identity)
}

func TestReadinessBackupsPreparedNativeWithoutRelease(t *testing.T) {
	dir := t.TempDir()
	state, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	request := readinessNativeReservation(t, "hello", data.AppIncarnationID(strings.Repeat("1", 32)))
	if _, err := state.ReserveEngine(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	assertReadinessBackupsUnknown(t, dir, target.Identity{ID: "fixture", HostKeyFingerprint: "fixture"})
}

func TestReadinessBackupsCrossedNativeIdentities(t *testing.T) {
	r := newNativeWithdrawalRig(t)
	dir := readinessNativeDirectory(t, r)
	before := diagnosticFiles(t, dir)
	snapshot, state := readinessCoverageSnapshot(t, dir, r.snapshot.Identity)
	for _, field := range []string{"app", "engine", "database", "binding", "incarnation", "name", "root", "directory"} {
		assertReadinessCrossedNative(t, snapshot, field)
	}
	crossed := state
	crossed.Releases = append([]plan.CurrentRelease(nil), state.Releases...)
	crossed.Releases[0].App = "another"
	if err := readinessDatabaseCoverage(t.Context(), snapshot, crossed); err == nil {
		t.Fatal("another app's plan supplied current-release coverage")
	}
	duplicate := state
	duplicate.Releases = append(append([]plan.CurrentRelease(nil), state.Releases...), state.Releases[0])
	if err := readinessDatabaseCoverage(t.Context(), snapshot, duplicate); err == nil {
		t.Fatal("duplicate expected binding overwritten")
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("crossed coverage changed live files")
	}
}

func assertReadinessCrossedNative(t *testing.T, snapshot *store.Store, field string) {
	t.Helper()
	bindings, err := snapshot.ReadReadinessBindings(t.Context(), readiness.MaxItems)
	if err != nil || len(bindings) != 1 {
		t.Fatal(bindings, err)
	}
	binding := bindings[0]
	switch field {
	case "app":
		binding.App = "another"
	case "engine":
		binding.Engine = data.EngineSQLite
	case "database":
		binding.SpacetimeDB.DatabaseID = data.DatabaseID(strings.Repeat("9", 32))
	case "binding":
		binding.SpacetimeDB.BindingID = data.ReplicaBindingID(strings.Repeat("9", 32))
	case "incarnation":
		binding.SpacetimeDB.IncarnationID = data.AppIncarnationID(strings.Repeat("9", 32))
	case "name":
		binding.SpacetimeDB.Name = "another"
	case "root":
		binding.SpacetimeDB.Root = "/other"
	case "directory":
		binding.SpacetimeDB.RelativeDirectory += "/other"
	}
	if err := readinessBindingCoverage(t.Context(), snapshot, map[store.ReadinessBinding]struct{}{binding: {}}); err == nil {
		t.Fatal("crossed native identity accepted", field)
	}
	// A matching row must not hide another expected, but absent, binding.
	extra := map[store.ReadinessBinding]struct{}{bindings[0]: {}, binding: {}}
	if err := readinessBindingCoverage(t.Context(), snapshot, extra); err == nil {
		t.Fatal("extra expected native binding accepted", field)
	}
}

func readinessNativeReservation(t *testing.T, app string, incarnation data.AppIncarnationID) store.EngineReservation {
	t.Helper()
	database := data.SpacetimeDBBinding{DatabaseID: data.DatabaseID(strings.Repeat("2", 32)), Name: "audit", IncarnationID: incarnation, Root: "/never-open-native", BindingID: data.ReplicaBindingID(strings.Repeat("3", 32))}
	var err error
	database.RelativeDirectory, err = data.RelativeDirectory(database.IncarnationID, database.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	destination := readinessReviewReservation("main", "/data").Destination
	epoch := data.ReplicaEpochID(strings.Repeat("4", 32))
	prefix, err := data.RemotePrefix(destination.BasePrefix, incarnation, database.DatabaseID, epoch)
	if err != nil {
		t.Fatal(err)
	}
	return store.EngineReservation{App: app, PolicyHash: "sha256:" + strings.Repeat("a", 64), Database: database, Archive: data.ArchiveBinding{BindingID: database.BindingID, DatabaseID: database.DatabaseID, Destination: destination, EpochID: epoch, RemotePrefix: prefix}}
}

func readinessCoverageSnapshot(t *testing.T, dir string, identity target.Identity) (*store.Store, plan.BrineState) {
	t.Helper()
	snapshot, err := store.OpenSnapshotReadOnly(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	state, err := readinessState(t.Context(), snapshot, identity)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, state
}

func readinessNativeDirectory(t *testing.T, r nativeWithdrawalRig) string {
	t.Helper()
	var seq int
	var name, file string
	if err := r.db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(file)
}

func assertReadinessBackupsUnknown(t *testing.T, dir string, identity target.Identity) {
	t.Helper()
	before := diagnosticFiles(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	backups, err := (readinessControl{dir: dir}).Backups(ctx, identity)
	if err == nil || backups != nil {
		t.Fatal("unrepresented preparation claimed complete backup coverage", backups, err)
	}
	if !reflect.DeepEqual(before, diagnosticFiles(t, dir)) {
		t.Fatal("refused backup coverage changed live files")
	}
}
