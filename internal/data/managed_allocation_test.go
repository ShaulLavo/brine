package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAllocationOwnsEmptyTreeWithoutSQLiteShape(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRoot(root); err != nil {
		t.Skipf("private local filesystem unavailable: %v", err)
	}
	incarnation, database := AppIncarnationID(strings.Repeat("a", 32)), DatabaseID(strings.Repeat("b", 32))
	relative, err := RelativeDirectory(incarnation, database)
	if err != nil {
		t.Fatal(err)
	}
	binding := SpacetimeDBBinding{DatabaseID: database, Name: "main", IncarnationID: incarnation, Root: PersistentRoot(root), RelativeDirectory: relative, BindingID: ReplicaBindingID(strings.Repeat("c", 32))}
	if _, err := PrepareSpacetimeDirectory(binding); err != nil {
		t.Fatal(err)
	}
	receipt, err := CaptureSpacetimeAllocation(binding)
	if err != nil || !receipt.Valid() {
		t.Fatal("empty native allocation missing", err)
	}
	entries, err := os.ReadDir(binding.SourcePath())
	if err != nil || len(entries) != 0 {
		t.Fatal("native allocation created a database or config", err)
	}
	if err := os.WriteFile(filepath.Join(binding.SourcePath(), "config.toml"), []byte("[module-http]\nenabled = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureSpacetimeAllocation(binding); err == nil {
		t.Fatal("nonempty native tree gained fresh allocation receipt")
	}
	changed := binding
	changed.RelativeDirectory = "../escape"
	if _, err := PrepareSpacetimeDirectory(changed); err == nil {
		t.Fatal("native hierarchy accepted unowned relative path")
	}
}
