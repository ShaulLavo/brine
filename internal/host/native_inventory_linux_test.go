//go:build linux

package host

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/klauspost/compress/zstd"
)

func nativeInventoryRoot(t *testing.T, raw []byte) (*os.Root, engineTreeSeal) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil { //nolint:gosec // Private directories require traversal permission.
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "replicas/7/clog"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "replicas/7/clog/00000000000000000000.stdb.log"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := engineOpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	seal, err := sealEngineRoot(context.Background(), root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	return root, seal
}
func TestNativeOwnedInventoryDerivesActualReplicaAndCompressedExpansion(t *testing.T) {
	payload := bytes.Repeat([]byte("native selected history"), 1000)
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(payload, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	root, seal := nativeInventoryRoot(t, compressed)
	inventory, err := nativeOwnedInventory(context.Background(), root, seal, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32))
	if err != nil || inventory.Scope().ReplicaID != 7 || len(inventory.Members()) != 1 || inventory.Members()[0].ExpandedBytes != uint64(len(payload)) || inventory.Members()[0].Bytes != uint64(len(compressed)) {
		t.Fatal("actual compressed census", err)
	}
	if err := root.MkdirAll("replicas/8/clog", 0700); err != nil {
		t.Fatal(err)
	}
	changed, err := sealEngineRoot(context.Background(), root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeSoleReplica(changed); err == nil {
		t.Fatal("multiple native replicas silently selected one")
	}
}
func TestNativeOwnedInventoryRejectsCorruptCompressedMember(t *testing.T) {
	root, seal := nativeInventoryRoot(t, []byte{0x28, 0xb5, 0x2f, 0xfd, 0xff, 0xff})
	if _, err := nativeOwnedInventory(context.Background(), root, seal, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)); err == nil {
		t.Fatal("corrupt zstd history produced an expansion budget")
	}
}
func TestNativeOwnedInventoryRefusesNoncanonicalReplica(t *testing.T) {
	root, _ := nativeInventoryRoot(t, []byte("native log history"))
	if err := root.Rename("replicas/7", "replicas/07"); err != nil {
		t.Fatal(err)
	}
	seal, err := sealEngineRoot(context.Background(), root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativeSoleReplica(seal); err == nil {
		t.Fatal("noncanonical replica directory admitted")
	}
}

func TestNativeOwnedInventoryExcludesOnlyPairedOffsetIndex(t *testing.T) {
	root, _ := nativeInventoryRoot(t, []byte("native log fixture"))
	cachePath := "replicas/7/clog/00000000000000000000.stdb.ofs"
	cache := make([]byte, 4<<20)
	if err := root.WriteFile(cachePath, cache, 0600); err != nil {
		t.Fatal(err)
	}
	seal, err := sealEngineRoot(t.Context(), root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := nativeOwnedInventory(t.Context(), root, seal, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32))
	if err != nil || len(inventory.Members()) != 1 || inventory.Members()[0].Path != "replicas/7/clog/00000000000000000000.stdb.log" {
		t.Fatal("derived offset cache entered recovery data or hid actual log", err)
	}
	selected, err := nativecapture.Select(inventory, nativecapture.DefaultLimits())
	tail, present := selected.TailMember()
	if err != nil || !present || tail.Path != inventory.Members()[0].Path {
		t.Fatal("cache exclusion lost full active-tail selection", err)
	}
	retained, err := root.ReadFile(cachePath)
	if err != nil || !bytes.Equal(retained, cache) {
		t.Fatal("inventory changed original offset cache", err)
	}
	if err = root.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"orphan.ofs", "00000000000000000001.stdb.ofs", "0000000000000000000.stdb.ofs", "00000000000000000000.stdb.unknown", "0000000000000000000.stdb.log"} {
		checkNativeInventoryUnknownMember(t, root, name)
	}
}

func checkNativeInventoryUnknownMember(t *testing.T, root *os.Root, name string) {
	t.Helper()
	path := "replicas/7/clog/" + name
	if err := root.WriteFile(path, []byte("unknown member"), 0600); err != nil {
		t.Fatal(err)
	}
	seal, err := sealEngineRoot(t.Context(), root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = nativeOwnedInventory(t.Context(), root, seal, strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)); err == nil {
		t.Fatal("unknown or unpaired member silently ignored", name)
	}
	if err = root.Remove(path); err != nil {
		t.Fatal(err)
	}
}
