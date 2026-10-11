//go:build linux

package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveMoveFixture(t *testing.T) (ArchiveTree, AppIncarnationID, ArchiveID) {
	t.Helper()
	root := t.TempDir()
	// #nosec G302 -- Private fixture directory requires owner traversal.
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	inc := AppIncarnationID(strings.Repeat("a", 32))
	id := ArchiveID(strings.Repeat("b", 32))
	if err := os.MkdirAll(filepath.Join(root, "apps", string(inc)), 0700); err != nil {
		t.Fatal(err)
	}
	tree, err := CaptureArchiveTree(PersistentRoot(root), inc, id)
	if err != nil {
		t.Fatal(err)
	}
	return tree, inc, id
}
func TestArchiveRenameNeverReplacesAnExistingArchive(t *testing.T) {
	tree, inc, id := archiveMoveFixture(t)
	destination := filepath.Join(string(tree.Root), tree.ArchiveRelative)
	if err := os.MkdirAll(destination, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err = RenameArchiveTree(tree, inc, id); err == nil {
		t.Fatal("existing archive replaced")
	}
	after, err := os.Lstat(destination)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("destination changed", err)
	}
	if _, err = os.Stat(filepath.Join(string(tree.Root), tree.SourceRelative)); err != nil {
		t.Fatal("source moved on refusal", err)
	}
}
func TestArchiveRenameRefusesSymlinkParentBeforeMutation(t *testing.T) {
	tree, inc, id := archiveMoveFixture(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(string(tree.Root), "archives")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUnarchivedTree(tree, inc, id); err == nil {
		t.Fatal("unsafe archive parent admitted")
	}
	if err := RenameArchiveTree(tree, inc, id); err == nil {
		t.Fatal("symlink parent followed")
	}
}
func TestArchiveRenamePreservesTreeIdentityAndRefusesReplacement(t *testing.T) {
	tree, inc, id := archiveMoveFixture(t)
	if err := RenameArchiveTree(tree, inc, id); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchivedTree(tree, inc, id); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(string(tree.Root), tree.ArchiveRelative)
	if err := os.Rename(destination, destination+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyArchivedTree(tree, inc, id); err == nil {
		t.Fatal("replaced archived inode admitted")
	}
}
