//go:build linux

package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeHistoricalWorkspaceCleanupRemovesDownloadAndSigningMaterial(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { //nolint:gosec // Private directories require traversal permission.
		t.Fatal(err)
	}
	parent, path, err := newNativeHistoricalRestoreParent(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Mkdir("download", 0700); err != nil {
		t.Fatal(err)
	}
	if err := parent.WriteFile("download/signing.key", []byte("disposable signing material"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := discardNativeWorkspace(parent, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("completed isolated restore retained plaintext workspace", err)
	}
}

func TestNativeWorkspaceCleanupRefusesReplacedDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil { //nolint:gosec // Private directories require traversal permission.
		t.Fatal(err)
	}
	parent, path, err := newNativeHistoricalRestoreParent(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := os.Rename(path, path+"-owned"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "preexisting")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := discardNativeWorkspace(parent, path); err == nil {
		t.Fatal("replaced workspace removed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("replacement owner data removed", err)
	}
}
