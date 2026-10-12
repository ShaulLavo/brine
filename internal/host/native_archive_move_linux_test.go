//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeArchiveWholeTreeMovePreservesOriginalAndRefusesReplacement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := engineOpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	source := "transitions/0123456789abcdef0123456789abcdef"
	destination := "archives/abcdef0123456789abcdef0123456789/native-transitions/0123456789abcdef0123456789abcdef"
	for _, name := range []string{source + "/original/control-db", source + "/download/recovery", filepath.Dir(destination)} {
		if err = engineMakePrivateDirectories(root, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{source + "/original/control-db/data", source + "/download/recovery/signing.key", source + "/.brine-restore-owner"} {
		if err = root.WriteFile(name, []byte("original immutable member"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	observer := engineTransitionTree{limits: engineCopyLimits{Bytes: 1 << 20, Files: 64, Depth: 12}}
	location, err := observer.inspectLocation(ctx, root, source)
	seal, ok := location.Tree()
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = renameNativeArchiveTree(root, source, destination, seal); err != nil {
		t.Fatal(err)
	}
	moved, err := observer.inspectLocation(ctx, root, destination)
	actual, ok := moved.Tree()
	if err != nil || !ok || !nativeArchiveMovedSeal(seal, actual) {
		t.Fatal("whole original identity changed", err)
	}
	if raw, err := root.ReadFile(destination + "/download/recovery/signing.key"); err != nil || string(raw) != "original immutable member" {
		t.Fatal("retained signing member lost", err)
	}
	if err = root.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err = renameNativeArchiveTree(root, source, destination, seal); err == nil {
		t.Fatal("replacement source replaced existing archive")
	}
	changed := actual
	changed.ContentsSHA256 = string(data.EngineSQLite)
	if nativeArchiveMovedSeal(seal, changed) {
		t.Fatal("changed contents admitted")
	}
}

func TestNativeArchiveMoveRefusesExistingDestination(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := engineOpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"transitions/owned", "archives/owned/native-transitions/owned"} {
		if err = engineMakePrivateDirectories(root, name); err != nil {
			t.Fatal(err)
		}
	}
	if err = root.WriteFile("transitions/owned/data", []byte("current original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = root.WriteFile("archives/owned/native-transitions/owned/foreign", []byte("retained destination"), 0600); err != nil {
		t.Fatal(err)
	}
	observer := engineTransitionTree{limits: engineCopyLimits{Bytes: 1 << 20, Files: 16, Depth: 8}}
	located, err := observer.inspectLocation(ctx, root, "transitions/owned")
	seal, ok := located.Tree()
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = renameNativeArchiveTree(root, "transitions/owned", "archives/owned/native-transitions/owned", seal); err == nil {
		t.Fatal("foreign destination replaced")
	}
	if raw, err := root.ReadFile("transitions/owned/data"); err != nil || string(raw) != "current original" {
		t.Fatal("original removed on refusal", err)
	}
	if raw, err := root.ReadFile("archives/owned/native-transitions/owned/foreign"); err != nil || string(raw) != "retained destination" {
		t.Fatal("destination changed on refusal", err)
	}
}
