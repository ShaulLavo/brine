//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"golang.org/x/sys/unix"
)

func engineSourceFixture(t *testing.T) (data.SpacetimeDBBinding, string) {
	t.Helper()
	root := t.TempDir()
	staging := t.TempDir()
	for _, directory := range []string{root, staging} {
		if err := os.Chmod(directory, 0700); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
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
		err = os.Link(source, filepath.Join(staging, "outside-alias"))
	case "permissions":
		err = os.Chmod(source, 0644) //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
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

func TestEngineCopyDisposalKeepsOnlyOriginalSeal(t *testing.T) {
	binding, staging := engineSourceFixture(t)
	ctx := context.Background()
	copy, err := copyEngineSource(ctx, binding, staging, strings.Repeat("d", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.discard() }()
	if err = copy.disposeStage(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(copy.path); !os.IsNotExist(err) {
		t.Fatal("private copy retained before live handoff", err)
	}
	if err = copy.sourceUnchanged(ctx); err != nil {
		t.Fatal("disposing private copy lost stopped source binding", err)
	}
}

func TestEngineCopyNeverFollowsReplacedMemberSymlink(t *testing.T) {
	binding, staging := engineSourceFixture(t)
	source, err := openEngineSource(binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenRoot(staging)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = destination.Close() }()
	if err = destination.Mkdir("control-db", 0700); err != nil {
		t.Fatal(err)
	}
	seal, err := sealEngineRoot(context.Background(), source, engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	var member engineTreeMember
	for _, candidate := range seal.members {
		if candidate.Name == "control-db/state" {
			member = candidate
		}
	}
	file := filepath.Join(binding.SourcePath(), "control-db/state")
	if err = os.Rename(file, file+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("state-preserved", file); err != nil {
		t.Fatal(err)
	}
	if copyEngineFile(context.Background(), source, destination, member) == nil {
		t.Fatal("member copy followed symlink to the sealed inode")
	}
}

func TestEngineOwnedAliasesBecomeIndependentPrivateFiles(t *testing.T) {
	binding, staging := engineSourceFixture(t)
	left := filepath.Join(binding.SourcePath(), "control-db/state")
	right := filepath.Join(binding.SourcePath(), "second-object")
	if err := os.Link(left, right); err != nil {
		t.Fatal(err)
	}
	copy, err := copyEngineSource(context.Background(), binding, staging, strings.Repeat("d", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal("fully rooted owned aliases refused", err)
	}
	defer func() { _ = copy.discard() }()
	first, err := copy.root.Stat("control-db/state")
	second, otherErr := copy.root.Stat("second-object")
	if err != nil || otherErr != nil || os.SameFile(first, second) {
		t.Fatal("private copy retained source hardlinks", err, otherErr)
	}
	if _, err = sealEngineRoot(context.Background(), copy.root, copy.limits); err != nil {
		t.Fatal("private copy was not strictly single-linked", err)
	}
	if err = os.Remove(right); err != nil {
		t.Fatal(err)
	}
	if copy.sourceUnchanged(context.Background()) == nil {
		t.Fatal("alias membership drift retained source proof")
	}
}

func TestEngineNativeSnapshotAliasesPhysical(t *testing.T) {
	path := os.Getenv("BRINE_ENGINE_NATIVE_ROOT")
	if path == "" {
		t.Skip("physical native corpus not supplied")
	}
	binding := data.SpacetimeDBBinding{DatabaseID: data.DatabaseID(strings.Repeat("a", 32)), IncarnationID: data.AppIncarnationID(strings.Repeat("b", 32)), BindingID: data.ReplicaBindingID(strings.Repeat("c", 32)), Root: data.PersistentRoot(path), Name: "physical"}
	relative, err := data.RelativeDirectory(binding.IncarnationID, binding.DatabaseID)
	if err != nil {
		t.Fatal(err)
	}
	binding.RelativeDirectory = relative
	stage := t.TempDir()
	if err = os.Chmod(stage, 0700); err != nil { //nolint:gosec // Private directory or deliberately unsafe-mode refusal fixture.
		t.Fatal(err)
	}
	copy, err := copyEngineSource(context.Background(), binding, stage, strings.Repeat("d", 32), engineCopyLimits{Bytes: 32 << 20, Files: 2048, Depth: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copy.discard() }()
	linked := 0
	for _, member := range copy.original.members {
		if member.Mode&unix.S_IFMT == unix.S_IFREG && member.Links > 1 && strings.Contains(member.Name, ".snapshot_dir/objects/") {
			linked++
		}
	}
	if linked == 0 {
		t.Fatal("fixture did not retain actual native snapshot object links")
	}
	private, err := sealEngineRoot(context.Background(), copy.root, copy.limits)
	if err != nil || !sameEngineContents(copy.original, private) {
		t.Fatal("native source did not flatten into complete private files", err)
	}
	if err = copy.sourceUnchanged(context.Background()); err != nil {
		t.Fatal("native source changed during private copy", err)
	}
	t.Logf("native linked snapshot paths=%d source paths=%d copied bytes=%d; private regular files Nlink1", linked, len(copy.original.members), copy.original.bytes)
}
