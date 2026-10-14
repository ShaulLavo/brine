//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeArchiveDataSealSurvivesMoveAndDetectsRetainedChanges(t *testing.T) {
	binding, _ := engineSourceFixture(t)
	base, err := engineOpenRoot(string(binding.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = base.Close() }()
	ctx := context.Background()
	observer := engineTransitionTree{limits: nativeArchiveTreeLimits()}
	location, err := observer.inspectLocation(ctx, base, binding.RelativeDirectory)
	if _, present := location.Tree(); err != nil || !present {
		t.Fatal("production archive tree limits refused an owned tree", err)
	}
	original, present, err := sealNativeArchiveLocation(ctx, base, binding.RelativeDirectory)
	if err != nil || !present {
		t.Fatal("owned source seal refused", err)
	}
	retained := filepath.Join("archives", strings.Repeat("e", 32), "databases", string(binding.DatabaseID))
	if err = engineMakePrivateDirectories(base, filepath.Dir(retained)); err != nil {
		t.Fatal(err)
	}
	if err = base.Rename(binding.RelativeDirectory, retained); err != nil {
		t.Fatal(err)
	}
	persistedSourceSeal := engineTransitionSealDigest(original)
	moved, present, err := sealNativeArchiveLocation(ctx, base, retained)
	if err != nil || !present || engineTransitionSealDigest(moved) != persistedSourceSeal {
		t.Fatal("whole native source changed at its retained location", err)
	}
	if _, present, err = sealNativeArchiveLocation(ctx, base, binding.RelativeDirectory); err != nil || present {
		t.Fatal("original source still present after movement", err)
	}
	if err = base.WriteFile(filepath.Join(retained, "control-db", "state"), []byte("changed-after-retirement"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := sealNativeArchiveLocation(ctx, base, retained)
	if err != nil || engineTransitionSealDigest(changed) == persistedSourceSeal {
		t.Fatal("changed retained bytes kept the original seal", err)
	}
	if err = base.RemoveAll(retained); err != nil {
		t.Fatal(err)
	}
	if err = base.Symlink(string(binding.Root), retained); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sealNativeArchiveLocation(ctx, base, retained); err == nil || os.IsNotExist(err) {
		t.Fatal("replacement symlink became original absence or source data", err)
	}
}
