package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestQuickCheckSnapshot(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "non_ok"}[damaged], func(t *testing.T) { testQuickCheckSnapshot(t, damaged) })
	}
}

func testQuickCheckSnapshot(t *testing.T, damaged bool) {
	ctx := t.Context()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.QuickCheck(ctx); err == nil {
		t.Fatal("writable store accepted")
	}
	if damaged {
		makeQuickCheckNonOK(t, s)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before := quickCheckFiles(t, dir)
	snapshot, err := OpenSnapshotReadOnly(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	good, err := snapshot.QuickCheck(ctx)
	if err != nil || good == damaged {
		t.Fatalf("healthy=%t err=%v", good, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if ok, err := snapshot.QuickCheck(canceled); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %t %v", ok, err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, quickCheckFiles(t, dir)) {
		t.Fatal("live control files or listing changed")
	}
}

func quickCheckFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name())) // #nosec G304 -- Owned private fixture directory, never host state.
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = string(raw)
	}
	return out
}

func makeQuickCheckNonOK(t *testing.T, s *Store) {
	t.Helper()
	for _, query := range []string{"CREATE TABLE check_fixture(x INTEGER)", "INSERT INTO check_fixture VALUES(NULL)", "PRAGMA writable_schema=ON", "UPDATE sqlite_master SET sql='CREATE TABLE check_fixture(x INTEGER NOT NULL)' WHERE name='check_fixture'"} {
		if _, err := s.db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
}
