package restic

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractionUsesHeldParentAfterPathReplacement(t *testing.T) {
	source, work := testTree(t)
	bundle, err := Seal(context.Background(), source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	retained := work + "-retained"
	if err := os.Rename(work, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(retained, filepath.Base(bundle.path))
	root, err := extractEnvelope(context.Background(), archive, parent, "restored", bundle.Manifest(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := os.Stat(filepath.Join(work, "restored")); !os.IsNotExist(err) {
		t.Fatal("replacement pathname received extracted data", err)
	}
	value, err := root.ReadFile("state")
	if err != nil || string(value) != "confirmed fixture state" {
		t.Fatal("held parent lost exact extracted state", err)
	}
}

func TestExtractionRefusesExistingSymlinkChild(t *testing.T) {
	source, work := testTree(t)
	bundle, err := Seal(context.Background(), source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	parent, err := os.OpenRoot(work)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Symlink(source, "restored"); err != nil {
		t.Fatal(err)
	}
	if _, err := extractEnvelope(context.Background(), bundle.path, parent, "restored", bundle.Manifest(), DefaultLimits()); err == nil {
		t.Fatal("existing symlink child accepted exact extraction")
	}
}
