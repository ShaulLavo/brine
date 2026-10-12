//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineCopyCleanupPreservesReplacedStage(t *testing.T) {
	for _, operation := range []string{"dispose", "discard"} {
		t.Run(operation, func(t *testing.T) { testEngineCopyReplacedStage(t, operation) })
	}
}

func testEngineCopyReplacedStage(t *testing.T, operation string) {
	t.Helper()
	binding, staging := engineSourceFixture(t)
	copy, err := copyEngineSource(t.Context(), binding, staging, strings.Repeat("e", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	original := copy.path
	retained := filepath.Join(staging, "retained-original")
	if err = os.Rename(original, retained); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(original, "unrelated-owned-data")
	if err = os.WriteFile(marker, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	cleanup := copy.disposeStage
	if operation == "discard" {
		cleanup = copy.discard
	}
	if err = cleanup(); err == nil {
		t.Error("replacement stage earned successful disposal")
	}
	content, statErr := os.ReadFile(marker) //nolint:gosec // Parent-created marker under this test's owned temporary stage.
	if statErr != nil || string(content) != "must survive" {
		t.Fatal("cleanup deleted replacement data", statErr)
	}
	if copy.root == nil || copy.parent == nil || copy.source == nil {
		t.Fatal("mismatched stage lost held reconciliation handles")
	}
	if _, err = sealEngineRoot(context.Background(), copy.root, copy.limits); err != nil {
		t.Fatal("original owned stage no longer available", err)
	}
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(retained, original); err != nil {
		t.Fatal(err)
	}
	if err = copy.discard(); err != nil {
		t.Fatal("restored exact owned stage could not be disposed", err)
	}
	if err = copy.discard(); err != nil {
		t.Fatal("completed disposal was not idempotent", err)
	}
}
