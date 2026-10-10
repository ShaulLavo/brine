package enroll

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLitestreamUndoResumesAfterRemovedVersionDirectory(t *testing.T) {
	base := t.TempDir()
	version := filepath.Join(base, "brine", "litestream", "0.5.17")
	if err := os.MkdirAll(version, 0755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(version, "litestream")
	if err := os.WriteFile(executable, []byte("fixture executable"), 0755); err != nil {
		t.Fatal(err)
	}
	h := host{r: hostRecord{Files: map[string]ownedFile{executable: {Hash: hash([]byte("fixture executable")), Mode: 0755}}}}
	interrupted := false
	step := Step{Name: "litestream", Undo: func(context.Context) error {
		if !interrupted {
			// Reproduce a crash after journal-owned fixture resources were removed
			// but before the enrollment engine cleared the durable step intent.
			interrupted = true
			if err := os.Remove(executable); err != nil {
				return err
			}
			if err := os.Remove(version); err != nil {
				return err
			}
			return errors.New("interrupted before undo checkpoint")
		}
		// This is the first executable boundary used by undoLitestream when
		// re-entering with its original file journal and a missing parent.
		return h.restoreFile(executable)
	}}
	journal := Journal{Phase: Enrolled, Intents: map[string]bool{"litestream": true}}
	store := &memoryStore{}
	if err := Undo(context.Background(), store, &journal, []Step{step}); err == nil {
		t.Fatal("missing interruption")
	}
	if !journal.Intents["litestream"] {
		t.Fatal("lost durable undo intent")
	}
	if err := Undo(context.Background(), store, &journal, []Step{step}); err != nil {
		t.Fatal("could not resume completed removal", err)
	}
	if journal.Phase != Removed || len(journal.Intents) != 0 {
		t.Fatal("undo did not converge")
	}
}

func TestUndoAbsentOriginalAndSurvivingResourcesStillRefused(t *testing.T) {
	for _, kind := range []string{"original-missing", "changed-file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "litestream")
			original := kind == "original-missing"
			if kind == "changed-file" {
				if err := os.WriteFile(path, []byte("unowned content"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				if err := os.Symlink(path+"-absent", path); err != nil {
					t.Fatal(err)
				}
			}
			h := host{r: hostRecord{Files: map[string]ownedFile{path: {Existed: original, Hash: hash([]byte("recorded content")), Mode: 0755}}}}
			if err := h.restoreFile(path); err == nil {
				t.Fatal("unsafe surviving/original state accepted")
			}
			if !original {
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("surviving resource changed", err)
				}
			}
		})
	}
}
