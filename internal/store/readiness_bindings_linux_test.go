//go:build linux

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestReadReadinessBindingsEngines(t *testing.T) {
	for _, kind := range []string{"empty", "sqlite", "native", "mixed"} {
		t.Run(kind, func(t *testing.T) { testReadinessBindingsEngines(t, kind) })
	}
}

func testReadinessBindingsEngines(t *testing.T, kind string) {
	t.Helper()
	s := openTest(t)
	want := []ReadinessBinding{}
	if kind == "native" || kind == "mixed" {
		request := engineReservation(t)
		request.App = "example"
		reserved, err := s.ReserveEngine(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, ReadinessBinding{App: request.App, Engine: data.EngineSpacetimeDB, SpacetimeDB: reserved.Database})
	}
	if kind == "sqlite" || kind == "mixed" {
		reserved, err := s.ReserveDatabase(t.Context(), dataRequest())
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, ReadinessBinding{App: "example", Engine: data.EngineSQLite, SQLite: reserved.Database})
	}
	if got, err := s.ReadReadinessBindings(t.Context(), 16); got != nil || !errors.Is(err, ErrInvalid) {
		t.Fatal("mutable store admitted", got, err)
	}
	assertReadinessProjection(t, s, want)
}

func assertReadinessProjection(t *testing.T, s *Store, want []ReadinessBinding) {
	t.Helper()
	// Keep the writer open so the private snapshot must include its WAL.
	before := quickCheckFiles(t, s.dir)
	snapshot, err := OpenSnapshotReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	got, err := snapshot.ReadReadinessBindings(t.Context(), 16)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("ownership projection differs", got, want, err)
	}
	if len(want) > 1 {
		got, err = snapshot.ReadReadinessBindings(t.Context(), len(want)-1)
		if got != nil || !errors.Is(err, ErrInvalid) {
			t.Fatal("truncated projection claimed complete", got, err)
		}
	}
	for _, limit := range []int{0, -1, 17} {
		if got, err := snapshot.ReadReadinessBindings(t.Context(), limit); got != nil || !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid limit admitted", got, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := snapshot.ReadReadinessBindings(ctx, 16); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read returned coverage", got, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, s.dir)) {
		t.Fatal("snapshot projection changed live files or directory entries")
	}
}

func TestReadReadinessBindingsRejectsUnrepresentedRows(t *testing.T) {
	for _, damage := range []string{"missing_engine", "missing_binding", "empty_incarnation", "crossed_owner", "crossed_database", "crossed_canonical", "unknown_engine"} {
		t.Run(damage, func(t *testing.T) { testReadinessBindingDamage(t, damage) })
	}
}

func testReadinessBindingDamage(t *testing.T, damage string) {
	t.Helper()
	s := openTest(t)
	reserved, err := s.ReserveDatabase(t.Context(), dataRequest())
	if err != nil {
		t.Fatal(err)
	}
	// Damage only the fixture's indexes. Production immutability stays intact.
	queries := map[string][]string{
		"missing_engine":    {"DROP TRIGGER data_engine_kinds_no_delete", "DELETE FROM data_engine_kinds"},
		"missing_binding":   {"DROP TRIGGER data_replica_bindings_no_delete", "DELETE FROM data_replica_bindings"},
		"empty_incarnation": {"INSERT INTO data_incarnations VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','empty')", "INSERT INTO data_active_incarnations VALUES('empty','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"},
		"crossed_owner":     {"DROP TRIGGER data_active_no_update", "UPDATE data_active_incarnations SET app='another'"},
		"crossed_database":  {"DROP TRIGGER data_databases_no_update", "UPDATE data_databases SET name='other'"},
		"crossed_canonical": {"DROP TRIGGER data_databases_no_update", "UPDATE data_databases SET canonical=json_set(canonical,'$.replica_binding_id','ffffffffffffffffffffffffffffffff')"},
		"unknown_engine":    {"PRAGMA ignore_check_constraints=ON", "DROP TRIGGER data_engine_kinds_no_update", "UPDATE data_engine_kinds SET engine='unknown'"},
	}
	for _, query := range queries[damage] {
		if _, err := s.db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(query, err)
		}
	}
	before := quickCheckFiles(t, s.dir)
	snapshot, err := OpenSnapshotReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if got, err := snapshot.ReadReadinessBindings(t.Context(), 16); err == nil || got != nil {
		t.Fatal("damaged active ownership claimed complete", reserved.Database.DatabaseID, got, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, s.dir)) {
		t.Fatal("refused projection changed live files")
	}
}

func TestReadReadinessBindingsItemLimit(t *testing.T) {
	s := openTest(t)
	for i := range 16 {
		request := dataRequest()
		request.Database.Name = data.DatabaseName(fmt.Sprintf("db-%d", i))
		request.Database.MountPath = data.ContainerMountPath(fmt.Sprintf("/db-%d", i))
		if _, err := s.ReserveDatabase(t.Context(), request); err != nil {
			t.Fatal(err)
		}
	}
	before := quickCheckFiles(t, s.dir)
	snapshot, err := OpenSnapshotReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if got, err := snapshot.ReadReadinessBindings(t.Context(), 16); err != nil || len(got) != 16 {
		t.Fatal("exact item limit refused", len(got), err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, s.dir)) {
		t.Fatal("at-limit read changed live files")
	}
	request := dataRequest()
	if _, err := s.ReserveDatabase(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	before = quickCheckFiles(t, s.dir)
	overflow, err := OpenSnapshotReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = overflow.Close() })
	if got, err := overflow.ReadReadinessBindings(t.Context(), 16); got != nil || !errors.Is(err, ErrInvalid) {
		t.Fatal("over-limit rows became a complete projection", got, err)
	}
	if err := overflow.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, s.dir)) {
		t.Fatal("over-limit read changed live files")
	}
}

func TestReadReadinessBindingsUsesExistingSnapshot(t *testing.T) {
	s := openTest(t)
	reserved, err := s.ReserveEngine(t.Context(), engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := OpenSnapshotReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if _, err := s.ReserveDatabase(t.Context(), dataRequest()); err != nil {
		t.Fatal(err)
	}
	before := quickCheckFiles(t, s.dir)
	got, err := snapshot.ReadReadinessBindings(t.Context(), 16)
	want := []ReadinessBinding{{App: "managed", Engine: data.EngineSpacetimeDB, SpacetimeDB: reserved.Database}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("read projection reopened the live store", got, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, s.dir)) {
		t.Fatal("snapshot read changed the newer live state")
	}
}

func TestReadReadinessBindingsRetiredIncarnationIsNotActive(t *testing.T) {
	s := openTest(t)
	if _, err := s.ReserveEngine(t.Context(), engineReservation(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "DELETE FROM data_active_incarnations"); err != nil {
		t.Fatal(err)
	}
	assertReadinessProjection(t, s, []ReadinessBinding{})
}

func TestReadReadinessBindingsRejectsCrossedNativeIndex(t *testing.T) {
	s := openTest(t)
	if _, err := s.ReserveEngine(t.Context(), engineReservation(t)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"DROP TRIGGER data_replica_bindings_no_update", "UPDATE data_replica_bindings SET id='ffffffffffffffffffffffffffffffff'"} {
		if _, err := s.db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	before := quickCheckFiles(t, s.dir)
	snapshot, err := OpenSnapshotReadOnly(t.Context(), s.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if got, err := snapshot.ReadReadinessBindings(t.Context(), 16); got != nil || err == nil {
		t.Fatal("crossed native index supplied coverage", got, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, s.dir)) {
		t.Fatal("crossed native read changed live state")
	}
}
