//go:build linux

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func privateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}
func TestRootProbeAndPrivateAllocation(t *testing.T) {
	root := privateRoot(t)
	initial, err := InspectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if initial.POSIXLocks || initial.DurableRename {
		t.Fatal("inspection invented probe evidence")
	}
	observed, err := ProbeRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !observed.Admits(PersistentRoot(root), 1) {
		t.Fatalf("probe not admitted: %+v", observed)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe artifacts: %v %v", entries, err)
	}
	incarnation, _ := NewID()
	database, _ := NewID()
	relative, _ := RelativeDirectory(AppIncarnationID(incarnation), DatabaseID(database))
	b := DatabaseBinding{IncarnationID: AppIncarnationID(incarnation), DatabaseID: DatabaseID(database), Root: PersistentRoot(root), RelativeDirectory: relative}
	allocated, err := PrepareDirectory(b)
	if err != nil {
		t.Fatal(err)
	}
	if allocated.Device != observed.Device {
		t.Fatal("allocation crossed filesystem")
	}
	source := filepath.Join(root, relative)
	if err := verifyPrivateTree(root, source); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatal("allocation initialized database")
	}
	if _, err := PrepareDirectory(b); err != nil {
		t.Fatal("idempotent directory allocation:", err)
	}
}
func TestRootProbeCancellationDoesNotCreateFiles(t *testing.T) {
	root := privateRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeRoot(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("cancelled probe created files")
	}
}
func TestRootAncestorsAndModesFailClosed(t *testing.T) {
	parent := privateRoot(t)
	root := filepath.Join(parent, "data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyRootAncestors(root); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRoot(alias); err == nil {
		t.Fatal("symlink root accepted")
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRoot(root); err == nil {
		t.Fatal("nonprivate root accepted")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if err := verifyRootAncestors(root); err == nil {
		t.Fatal("writable ancestor accepted")
	}
}

func TestAllocationRejectsSymlinkBeforeCreatingDescendants(t *testing.T) {
	root := privateRoot(t)
	other := filepath.Join(root, "unrelated")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "apps")); err != nil {
		t.Fatal(err)
	}
	incarnation := AppIncarnationID("11111111111111111111111111111111")
	database := DatabaseID("22222222222222222222222222222222")
	relative, _ := RelativeDirectory(incarnation, database)
	if _, err := PrepareDirectory(DatabaseBinding{IncarnationID: incarnation, DatabaseID: database, Root: PersistentRoot(root), RelativeDirectory: relative}); err == nil {
		t.Fatal("symlink accepted")
	}
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatalf("allocation affected symlink target: %v %v", entries, err)
	}
}
