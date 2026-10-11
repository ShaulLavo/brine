//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func engineSourceFixture(t *testing.T) (data.SpacetimeDBBinding, string) {
	t.Helper()
	root := t.TempDir()
	staging := t.TempDir()
	for _, directory := range []string{root, staging} {
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binding := data.SpacetimeDBBinding{DatabaseID: data.DatabaseID(strings.Repeat("a", 32)), Name: "game", IncarnationID: data.AppIncarnationID(strings.Repeat("b", 32)), Root: data.PersistentRoot(root), BindingID: data.ReplicaBindingID(strings.Repeat("c", 32))}
	relative, err := data.RelativeDirectory(binding.IncarnationID, binding.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	binding.RelativeDirectory = relative
	if err = os.MkdirAll(filepath.Join(binding.SourcePath(), "control-db"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(binding.SourcePath(), "control-db", "state"), []byte("synthetic-persisted-state"), 0600); err != nil {
		t.Fatal(err)
	}
	return binding, staging
}

func TestEngineCopyBindsBytesAndCurrentSourcePath(t *testing.T) {
	binding, staging := engineSourceFixture(t)
	ctx := context.Background()
	copy, err := copyEngineSource(ctx, binding, staging, strings.Repeat("d", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.discard() }()
	content, err := copy.root.ReadFile("control-db/state")
	if err != nil || string(content) != "synthetic-persisted-state" || copy.sourceUnchanged(ctx) != nil {
		t.Fatal("private copy did not preserve original bytes", err)
	}
	if err = os.Rename(binding.SourcePath(), binding.SourcePath()+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(binding.SourcePath(), 0700); err != nil {
		t.Fatal(err)
	}
	if copy.sourceUnchanged(ctx) == nil {
		t.Fatal("pinned old descriptor hid replacement of the live source path")
	}
}

func TestEngineCopyCollisionDoesNotDeleteExistingStaging(t *testing.T) {
	binding, staging := engineSourceFixture(t)
	name := strings.Repeat("d", 32)
	if err := os.Mkdir(filepath.Join(staging, name), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(staging)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	marker := filepath.Join(name, "preserve")
	if err = root.WriteFile(marker, []byte("existing attempt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = copyEngineSource(context.Background(), binding, staging, name, engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16}); err == nil {
		t.Fatal("existing stage was adopted")
	}
	content, err := root.ReadFile(marker)
	if err != nil || string(content) != "existing attempt" {
		t.Fatal("failed copy deleted another attempt", err)
	}
}

func TestEngineSealRefusesUnsafeMembersAndBudgetOverflow(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink", "permissions", "bytes", "files", "depth"} {
		t.Run(attack, func(t *testing.T) { testEngineSealAttack(t, attack) })
	}
}

func testEngineSealAttack(t *testing.T, attack string) {
	t.Helper()
	binding, staging := engineSourceFixture(t)
	source := filepath.Join(binding.SourcePath(), "control-db", "state")
	limits := engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16}
	var err error
	switch attack {
	case "symlink":
		err = os.Symlink("control-db/state", filepath.Join(binding.SourcePath(), "alias"))
	case "hardlink":
		err = os.Link(source, filepath.Join(binding.SourcePath(), "alias"))
	case "permissions":
		err = os.Chmod(source, 0644)
	case "bytes":
		limits.Bytes = 1
	case "files":
		limits.Files = 1
	case "depth":
		limits.Depth = 1
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = copyEngineSource(context.Background(), binding, staging, strings.Repeat("d", 32), limits); err == nil {
		t.Fatal("unsafe source was copied")
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatal("source was removed after refusal", err)
	}
}

func TestEngineSourceDriftRefusesBeforeLaunch(t *testing.T) {
	binding, staging := engineSourceFixture(t)
	ctx := context.Background()
	copy, err := copyEngineSource(ctx, binding, staging, strings.Repeat("d", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.discard() }()
	if err = os.WriteFile(filepath.Join(binding.SourcePath(), "control-db", "state"), []byte("changed-persisted-state"), 0600); err != nil {
		t.Fatal(err)
	}
	if copy.sourceUnchanged(ctx) == nil {
		t.Fatal("changed source retained startup authority")
	}
}
