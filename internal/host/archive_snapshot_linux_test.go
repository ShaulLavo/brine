//go:build linux

package host

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestArchiveSnapshotPreservesCommittedRowsWithoutTouchingLiveDatabase(t *testing.T) {
	ctx := context.Background()
	// Persistent roots and archive staging must be on a real filesystem;
	// t.TempDir may be tmpfs, which persistent data records refuse.
	root, err := os.MkdirTemp("/var/tmp", "brine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// #nosec G302 -- Private fixture directory requires owner traversal.
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	inc := data.AppIncarnationID(strings.Repeat("a", 32))
	id := data.DatabaseID(strings.Repeat("b", 32))
	relative, err := data.RelativeDirectory(inc, id)
	if err != nil {
		t.Fatal(err)
	}
	binding := data.DatabaseBinding{DatabaseID: id, IncarnationID: inc, Root: data.PersistentRoot(root), RelativeDirectory: relative, Name: "main", MountPath: "/data", Filename: "app.sqlite", ReplicaBindingID: data.ReplicaBindingID(strings.Repeat("c", 32))}
	directory := filepath.Join(root, relative)
	if err = os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := data.CaptureAllocation(binding)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "app.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, "CREATE TABLE writes(value TEXT); INSERT INTO writes VALUES ('last-commit')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(source, 0600); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- Exact SQLite fixture path beneath this test's temporary directory.
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("/var/tmp", "brine-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	h := archiveHost{stateRoot: state}
	// #nosec G302 -- Private fixture state requires owner traversal.
	if err = os.Chmod(h.stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := h.snapshot(ctx, archive.Database{Binding: binding, Allocation: &allocation}, strings.Repeat("d", 32))
	if err != nil {
		t.Fatal("snapshot", err)
	}
	output := filepath.Join(t.TempDir(), "restored.sqlite")
	if err = os.WriteFile(output, raw, 0600); err != nil {
		t.Fatal(err)
	}
	copyDB, err := sql.Open("sqlite", output)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	queryErr := copyDB.QueryRowContext(ctx, "SELECT value FROM writes").Scan(&value)
	if closeErr := copyDB.Close(); queryErr != nil || closeErr != nil || value != "last-commit" {
		t.Fatal("snapshot lost row", queryErr, closeErr, value)
	}
	// #nosec G304 -- Exact SQLite fixture path beneath this test's temporary directory.
	after, err := os.ReadFile(source)
	if err != nil || string(before) != string(after) {
		t.Fatal("snapshot altered source", err)
	}
	entries, err := os.ReadDir(filepath.Join(h.stateRoot, "archive-snapshots"))
	if err != nil || len(entries) != 0 {
		t.Fatal("snapshot workspace leaked", err)
	}
}
