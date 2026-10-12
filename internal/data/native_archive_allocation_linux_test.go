//go:build linux

package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeArchiveAllocationKeepsIdentityThroughMoveAndRefusesReplacement(t *testing.T) {
	tree, inc, archiveID := archiveMoveFixture(t)
	database := DatabaseID(strings.Repeat("c", 32))
	relative, err := RelativeDirectory(inc, database)
	if err != nil {
		t.Fatal(err)
	}
	binding := SpacetimeDBBinding{DatabaseID: database, Name: "db", IncarnationID: inc, Root: tree.Root, RelativeDirectory: relative, BindingID: ReplicaBindingID(strings.Repeat("d", 32))}
	if err = os.MkdirAll(binding.SourcePath(), 0700); err != nil {
		t.Fatal(err)
	}
	dev, inode, err := directoryIdentity(binding.SourcePath())
	if err != nil {
		t.Fatal(err)
	}
	allocation := AllocationReceipt{DatabaseID: database, IncarnationID: inc, RootDevice: tree.RootDevice, RootInode: tree.RootInode, DirectoryDevice: dev, DirectoryInode: inode, AllocatedAt: time.Now().UTC()}
	if err = VerifyNativeArchiveAllocation(binding, allocation); err != nil {
		t.Fatal(err)
	}
	if err = RenameArchiveTree(tree, inc, archiveID); err != nil {
		t.Fatal(err)
	}
	if err = VerifyNativeArchivedAllocation(binding, allocation, archiveID); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(string(tree.Root), "archives", string(archiveID), "databases", string(database))
	if err = os.Rename(archived, archived+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(archived, 0700); err != nil {
		t.Fatal(err)
	}
	if err = VerifyNativeArchivedAllocation(binding, allocation, archiveID); err == nil {
		t.Fatal("replaced native archive allocation admitted")
	}
	if err = VerifyNativeArchiveAllocation(binding, allocation); err == nil {
		t.Fatal("archived native source still admitted as live")
	}
}
