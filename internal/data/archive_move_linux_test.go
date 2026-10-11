//go:build linux

package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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

func TestArchiveRenameSymlinkSwapCannotMoveOutsideData(t *testing.T) {
	for _, swap := range []string{"apps", "archives", "both"} {
		t.Run(swap, func(t *testing.T) {
			tree, inc, id := archiveMoveFixture(t)
			outside := t.TempDir()
			for _, name := range []string{"apps", "archives"} {
				if err := os.Mkdir(filepath.Join(outside, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			foreignSource := filepath.Join(outside, "apps", string(inc))
			if err := os.Mkdir(foreignSource, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(foreignSource, "sentinel"), []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			foreignBefore, err := os.Stat(foreignSource)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			err = renameArchiveTree(tree, inc, id, func(fromFD int, from string, toFD int, to string, flags uint) error {
				called = true
				for _, name := range []string{"apps", "archives"} {
					if swap != name && swap != "both" {
						continue
					}
					parent := filepath.Join(string(tree.Root), name)
					if err := os.Rename(parent, parent+"-pinned"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(outside, name), parent); err != nil {
						t.Fatal(err)
					}
				}
				return unix.Renameat2(fromFD, from, toFD, to, flags)
			})
			if !called || err == nil {
				t.Fatal("swapped parents not detected", err)
			}
			foreignAfter, statErr := os.Stat(foreignSource)
			if statErr != nil || !os.SameFile(foreignBefore, foreignAfter) {
				t.Fatal("moved outside source", statErr)
			}
			if _, statErr = os.Lstat(filepath.Join(outside, "archives", string(id))); !os.IsNotExist(statErr) {
				t.Fatal("wrote outside destination", statErr)
			}
			// #nosec G304 -- Fixed sentinel filename under this test's private outside fixture.
			if raw, readErr := os.ReadFile(filepath.Join(foreignSource, "sentinel")); readErr != nil || string(raw) != "outside" {
				t.Fatal("changed outside bytes", readErr)
			}
			archiveParent := "archives"
			if swap == "archives" || swap == "both" {
				archiveParent += "-pinned"
			}
			device, inode, identityErr := directoryIdentity(filepath.Join(string(tree.Root), archiveParent, string(id)))
			if identityErr != nil || device != tree.SourceDevice || inode != tree.SourceInode {
				t.Fatal("lost the recorded tree in pinned parents", identityErr)
			}

		})
	}
}
