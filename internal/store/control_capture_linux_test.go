//go:build linux

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestControlCaptureAbsent(t *testing.T) {
	dir := stateDir(t)
	scratch := t.TempDir()
	before := captureTree(t, dir)
	c, err := CaptureBytes(context.Background(), dir, os.Geteuid())
	if err != nil || !c.Absent {
		t.Fatalf("capture absence: %+v %v", c, err)
	}
	f, err := InspectCapture(context.Background(), c, scratch)
	if err != nil || !f.Absent {
		t.Fatalf("inspect absence: %+v %v", f, err)
	}
	data, f, err := RehearseMigration(context.Background(), c, scratch)
	if err != nil || data != nil || !f.Absent {
		t.Fatalf("rehearse absence: %+v %v", f, err)
	}
	assertCaptureTree(t, dir, before)
	assertCaptureScratchEmpty(t, scratch)
	if _, err := CaptureBytes(context.Background(), filepath.Join(dir, "missing"), os.Geteuid()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
}

func TestControlCaptureWALAndRehearsal(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.db.Exec("PRAGMA wal_autocheckpoint=0; CREATE TABLE capture_probe(value TEXT); INSERT INTO capture_probe VALUES('committed in WAL')"); err != nil {
		t.Fatal(err)
	}
	before := captureTree(t, s.dir)
	c, err := CaptureBytes(ctx, s.dir, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.WAL) == 0 || c.DBID.SHA256 != captureDigest(c.DB) || c.WALID.SHA256 != captureDigest(c.WAL) {
		t.Fatal("missing raw WAL or byte identity")
	}
	scratch := t.TempDir()
	f, err := InspectCapture(ctx, c, scratch)
	if err != nil || f.Version != SchemaVersion || !f.Integrity || !f.ForeignKeys {
		t.Fatalf("inspect: %+v %v", f, err)
	}
	data, f, err := RehearseMigration(ctx, c, scratch)
	if err != nil || f.Version != SchemaVersion || !f.Integrity || !f.ForeignKeys {
		t.Fatalf("rehearse: %+v %v", f, err)
	}
	normalized := stateDir(t)
	if err := os.WriteFile(filepath.Join(normalized, "control.db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	read, err := OpenReadOnly(ctx, normalized)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	var value string
	if err := read.db.QueryRow("SELECT value FROM capture_probe").Scan(&value); err != nil || value != "committed in WAL" {
		t.Fatalf("WAL transaction lost: %q %v", value, err)
	}
	assertCaptureTree(t, s.dir, before)
	assertCaptureScratchEmpty(t, scratch)
}

func captureDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type captureTreeEntry struct {
	Data []byte
	Info os.FileInfo
}

func captureTree(t *testing.T, dir string) map[string]captureTreeEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	out := map[string]captureTreeEntry{}
	for _, entry := range entries {
		data, err := root.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = captureTreeEntry{data, info}
	}
	return out
}

func assertCaptureTree(t *testing.T, dir string, before map[string]captureTreeEntry) {
	t.Helper()
	after := captureTree(t, dir)
	if len(after) != len(before) {
		t.Fatalf("file presence changed: %v -> %v", before, after)
	}
	for name, original := range before {
		current, ok := after[name]
		if !ok || !bytes.Equal(original.Data, current.Data) || !os.SameFile(original.Info, current.Info) || original.Info.Mode() != current.Info.Mode() || !original.Info.ModTime().Equal(current.Info.ModTime()) {
			t.Fatalf("source %s changed", name)
		}
	}
}

func assertCaptureScratchEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || !reflect.DeepEqual(entries, []os.DirEntry{}) {
		t.Fatalf("scratch leaked: %v %v", entries, err)
	}
}
