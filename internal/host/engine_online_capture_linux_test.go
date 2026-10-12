//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func onlineFileFixture(t *testing.T) (*os.Root, *os.File, engineTreeMember) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "head"), []byte("captured prefix"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	file, err := engineOpenMember(root, "head", unix.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	member, err := engineSourceMember(file, "head")
	if err != nil {
		t.Fatal(err)
	}
	return root, file, member
}

func TestEngineCapturedPrefixAllowsAppendAndRejectsRewrite(t *testing.T) {
	root, file, original := onlineFileFixture(t)
	ctx := context.Background()
	hash, err := hashEngineCapturedPrefix(ctx, file, original, original.Size)
	if err != nil {
		t.Fatal(err)
	}
	original.SHA256 = hash
	appendFile, err := root.OpenFile("head", os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendFile.WriteString(" later append"); err != nil {
		t.Fatal(err)
	}
	if err := appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkEngineCapturedPrefix(ctx, root, file, original, original.Size, hash); err != nil {
		t.Fatal("append outside boundary invalidated prefix", err)
	}
	rewrite, err := root.OpenFile("head", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rewrite.WriteAt([]byte("changed"), 0); err != nil {
		t.Fatal(err)
	}
	if err := rewrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkEngineCapturedPrefix(ctx, root, file, original, original.Size, hash); err == nil {
		t.Fatal("rewritten owned prefix admitted")
	}
}

func TestEngineCapturedPrefixRejectsPathReplacementAndShrink(t *testing.T) {
	root, file, original := onlineFileFixture(t)
	ctx := context.Background()
	hash, err := hashEngineCapturedPrefix(ctx, file, original, original.Size)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("head", "retained"); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("head", []byte("captured prefix"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkEngineCapturedPrefix(ctx, root, file, original, original.Size, hash); err == nil {
		t.Fatal("replaced path admitted despite retained descriptor")
	}
	if err := root.WriteFile("retained", []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hashEngineCapturedPrefix(ctx, file, original, original.Size); err == nil {
		t.Fatal("shrunk retained file admitted")
	}
}

func TestEngineOnlineCensusRequiresCompleteOwnedAliases(t *testing.T) {
	root, _, _ := onlineFileFixture(t)
	ctx := context.Background()
	limits := engineCopyLimits{Bytes: 1 << 20, Files: 16, Depth: 8}
	before, err := scanEngineOnlineMetadata(ctx, root, limits)
	if err != nil || len(before.members) != 1 || before.members[0].SHA256 != "" {
		t.Fatal("metadata census manufactured content seal", err)
	}
	if err := root.Link("head", "alias"); err != nil {
		t.Fatal(err)
	}
	after, err := scanEngineOnlineMetadata(ctx, root, limits)
	if err != nil || len(after.members) != 2 || after.members[0].Links != 2 {
		t.Fatal("complete internal alias census refused", err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.Link(filepath.Join(root.Name(), "head"), external); err != nil {
		t.Fatal(err)
	}
	if _, err := scanEngineOnlineMetadata(ctx, root, limits); err == nil {
		t.Fatal("external alias omitted from owned census was admitted")
	}
}

func TestEngineOnlineCensusRefusesWholeTailRotation(t *testing.T) {
	root, _, original := onlineFileFixture(t)
	ctx := context.Background()
	limits := engineCopyLimits{Bytes: 1 << 20, Files: 16, Depth: 8}
	before, err := scanEngineOnlineMetadata(ctx, root, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("later-tail", []byte("later complete tail"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := scanEngineOnlineMetadata(ctx, root, limits)
	if err != nil {
		t.Fatal(err)
	}
	if engineOnlineCensusMatches(before, after, "head", original.Size) {
		t.Fatal("new complete tail ignored")
	}
	if engineOnlineCensusMatches(after, before, "head", original.Size) {
		t.Fatal("removed complete tail ignored")
	}
}

func TestEngineCapturedPrefixCopiesExactlyBoundary(t *testing.T) {
	root, source, original := onlineFileFixture(t)
	if err := root.WriteFile("head", []byte("captured prefix later bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	destination, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = destination.Close() }()
	copied, err := copyEngineCapturedPrefix(context.Background(), source, original, original.Size, destination, "captured")
	if err != nil || copied.Size != original.Size || copied.Links != 1 {
		t.Fatal("captured prefix copy failed", err)
	}
	raw, err := destination.ReadFile("captured")
	if err != nil || string(raw) != "captured prefix" {
		t.Fatal("copy consumed bytes after captured boundary", err)
	}
	if _, err := copyEngineCapturedPrefix(context.Background(), source, original, original.Size, destination, "captured"); err == nil {
		t.Fatal("existing stage member replaced")
	}
}
