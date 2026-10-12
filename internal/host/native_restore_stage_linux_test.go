//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRestoreStageRefusesMissingExecutionBeforeCreatingDirectories(t *testing.T) {
	if stage, err := newNativeRestoreStage(context.Background(), nil, nil, engineSealedCheckpoint{}); err == nil || stage != nil {
		t.Fatal("missing actual20 execution created a restore stage")
	}
	if err := (*nativeRestoreStage)(nil).unchanged(context.Background()); err == nil {
		t.Fatal("nil stage admitted extraction")
	}
}

func TestNativeRestoreStagePinsDirectoryIdentityAndRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := engineOpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err = root.Mkdir("owned", 0700); err != nil {
		t.Fatal(err)
	}
	before, err := nativeRestoreDirectoryIdentity(root, "owned")
	if err != nil {
		t.Fatal(err)
	}
	if err = root.Rename("owned", "retained"); err != nil {
		t.Fatal(err)
	}
	if err = root.Mkdir("owned", 0700); err != nil {
		t.Fatal(err)
	}
	after, err := nativeRestoreDirectoryIdentity(root, "owned")
	if err != nil || sameNativeRestoreDirectory(before, after) {
		t.Fatal("replacement inode matched retained identity", err)
	}
	if err = os.Symlink(filepath.Join(dir, "retained"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err = nativeRestoreDirectoryIdentity(root, "alias"); err == nil {
		t.Fatal("symlink parent admitted")
	}
}
