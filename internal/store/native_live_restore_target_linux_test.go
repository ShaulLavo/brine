//go:build linux

package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

func TestNativeRestoreTargetTreeAnchorRefusesReplacedDirectoryAndMountAliases(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(directory, "transitions", "owned", "staged"), 0700); err != nil {
		t.Fatal(err)
	}
	base, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(base) }()
	root, err := nativeRestoreStatDirectory(base)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := nativeRestoreOpenDirectory(base, "transitions/owned")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(parent) }()
	p, err := nativeRestoreStatDirectory(parent)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := nativeRestoreOpenDirectory(parent, "staged")
	if err != nil {
		t.Fatal(err)
	}
	l, err := nativeRestoreStatDirectory(leaf)
	_ = unix.Close(leaf)
	if err != nil {
		t.Fatal(err)
	}
	tree := data.EngineTreeSeal{Root: nativeRestoreDirectoryID(root), Parent: nativeRestoreDirectoryID(p), Directory: nativeRestoreDirectoryID(l), ContentsSHA256: strings.Repeat("a", 64)}
	if err = checkNativeRestoreTreeAnchor(base, root, "transitions/owned/staged", tree); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(directory, "transitions", "owned", "staged"), filepath.Join(directory, "transitions", "owned", "retained")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(directory, "transitions", "owned", "staged"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = checkNativeRestoreTreeAnchor(base, root, "transitions/owned/staged", tree); err == nil {
		t.Fatal("replacement target reused retained inode")
	}
	if err = os.Symlink(filepath.Join(directory, "transitions", "owned", "retained"), filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if err = checkNativeRestoreTreeAnchor(base, root, "alias", tree); err == nil {
		t.Fatal("symlink tree admitted")
	}
}

func TestNativeRestoreTargetAndTransitionRefuseUnqualifiedCapabilities(t *testing.T) {
	f, _, _ := storedNativeRestoreJournalFixture(t)
	ctx := context.Background()
	if _, err := json.Marshal(NativeRestoreTargetObservation{}); err == nil {
		t.Fatal("target observation became RPC authority")
	}
	if _, err := json.Marshal(NativeRestoreTransitionObservation{}); err == nil {
		t.Fatal("completion observation became RPC authority")
	}
	if _, err := f.store.ReadNativeRestoreTransition(ctx, &NativeRestoreTransition{}); err == nil {
		t.Fatal("syntax capability admitted")
	}
	if _, err := f.store.ActivateNativeLiveRestore(ctx, nil, NativeRestoreTransitionObservation{}); err == nil {
		t.Fatal("missing original claim activated head")
	}
	if _, err := f.store.CompleteNativeLiveRestore(ctx, nil, NativeRestoreTransitionObservation{}); err == nil {
		t.Fatal("missing original claim released fence")
	}
	if err := f.store.CheckNativeLiveRestoreRetainedSource(ctx, nil); err == nil {
		t.Fatal("missing extraction admitted")
	}
}
