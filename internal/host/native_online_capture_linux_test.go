//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func nativeOnlineCopyFixture(t *testing.T) (*nativeOnlineCopy, string, *os.Root) {
	t.Helper()
	binding, staging := engineSourceFixture(t)
	parent, err := engineOpenRoot(staging)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close() })
	source, err := openEngineSource(binding)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := nativeRestoreDirectoryIdentity(source, ".")
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	permit := store.EnginePermit{Reserved: store.ReservedEngine{Database: binding}, Allocation: &data.AllocationReceipt{DirectoryDevice: identity.Device, DirectoryInode: identity.Inode}}
	copy, err := captureNativeOnlineCopy(context.Background(), permit, parent, staging)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { copy.close() })
	return copy, binding.SourcePath(), parent
}
func TestNativeOnlineCopyRejectsControlMetadataAppend(t *testing.T) {
	copy, source, _ := nativeOnlineCopyFixture(t)
	name := filepath.Join(source, "control-db/state")
	file, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Write([]byte("new control metadata"))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if copy.unchanged(context.Background()) == nil {
		t.Fatal("online metadata checkpoint admitted appended control database while fixed metadata contract requires drift refusal")
	}
}
func TestNativeOnlineCopyRemovesOwnedScratch(t *testing.T) {
	copy, _, parent := nativeOnlineCopyFixture(t)
	payload, err := copy.root.ReadFile("control-db/state")
	if err != nil {
		t.Fatal(err)
	}
	if err = copy.close(); err != nil {
		t.Fatal(err)
	}
	retained, err := parent.ReadFile("source/control-db/state")
	if err == nil && string(retained) == string(payload) {
		t.Fatal("routine capture close retains plaintext source tree; every scheduled capture accumulates another nonce without expiry or cleanup owner")
	}
}
