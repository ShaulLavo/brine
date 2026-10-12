//go:build linux

package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNativeRestoreOwnerMarkerBindsIntentAndRejectsAliases(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, ".brine-restore-owner")
	nonce := strings.Repeat("a", 64)
	if err := os.WriteFile(marker, []byte(nonce), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err = nativeRestoreOwnerMarker(int(file.Fd()), nonce); err != nil {
		t.Fatal(err)
	}
	if err = nativeRestoreOwnerMarker(int(file.Fd()), strings.Repeat("b", 64)); err == nil {
		t.Fatal("another intent reused the parent marker")
	}
	if err = os.Link(marker, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if err = nativeRestoreOwnerMarker(int(file.Fd()), nonce); err == nil {
		t.Fatal("aliased marker admitted")
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(directory, "foreign")); err != nil {
		t.Fatal(err)
	}
	if fd, openErr := nativeRestoreOpenDirectory(int(file.Fd()), "foreign"); openErr == nil {
		_ = unix.Close(fd)
		t.Fatal("foreign symlink parent admitted")
	}
}

func TestNativeRestoreObservationCannotBeUsedAsSerializedProof(t *testing.T) {
	if _, err := json.Marshal(NativeRestoreObservation{}); err == nil {
		t.Fatal("trusted host observation became an RPC proof body")
	}
}
