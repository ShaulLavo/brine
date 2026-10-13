//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateSourceCopyCreatesMissingStagingAndOwnsCleanup(t *testing.T) {
	binding, stateRoot := engineSourceFixture(t)
	ctx := context.Background()
	path := filepath.Join(stateRoot, "staging")
	limits := engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16}
	if copied, err := copyEngineSource(ctx, binding, path, strings.Repeat("d", 32), limits); err == nil || copied != nil {
		t.Fatal("missing parent unexpectedly satisfied the old copy boundary")
	}
	copied, err := copyEngineStateSource(ctx, binding, stateRoot, strings.Repeat("d", 32), limits)
	if err != nil {
		t.Fatal("missing staging parent still blocked source copy", err)
	}
	content, err := copied.root.ReadFile("control-db/state")
	if err != nil || string(content) != "synthetic-persisted-state" || copied.sourceUnchanged(ctx) != nil {
		t.Fatal("source copy did not retain original bytes", err)
	}
	if err := copied.discard(); err != nil {
		t.Fatal("source staging cleanup", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("task-created empty staging parent remained", err)
	}
}

func TestStateNativeSelectionCreatesMissingStagingAndOwnsCleanup(t *testing.T) {
	source, selection, _ := nativeViewFixture(t)
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0700); err != nil { //nolint:gosec // Private fixture state root.
		t.Fatal(err)
	}
	view, err := materializeNativeStateSelection(context.Background(), source, stateRoot, selection, strings.Repeat("e", 32))
	if err != nil || view.unchanged(context.Background()) != nil {
		t.Fatal("selection did not materialize through missing parent", err)
	}
	if err := view.close(); err != nil {
		t.Fatal("selection staging cleanup", err)
	}
	if _, err := os.Lstat(filepath.Join(stateRoot, "staging")); !os.IsNotExist(err) {
		t.Fatal("selection left its new staging parent", err)
	}
}

func TestStateStagingPreservesExistingParentAndContents(t *testing.T) {
	binding, stateRoot := engineSourceFixture(t)
	path := filepath.Join(stateRoot, "staging")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "existing")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil { //nolint:gosec // Owner's synthetic fixture content.
		t.Fatal(err)
	}
	copied, err := copyEngineStateSource(context.Background(), binding, stateRoot, strings.Repeat("d", 32), engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16})
	if err != nil {
		t.Fatal(err)
	}
	if err := copied.discard(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(marker) //nolint:gosec // Parent-owned synthetic fixture marker.
	if err != nil || string(content) != "keep" {
		t.Fatal("existing staging parent or contents were removed", err)
	}
}

func TestStateStagingRefusesUnsafeParentAndNode(t *testing.T) {
	for _, kind := range []string{"parent_mode", "parent_symlink", "staging_mode", "staging_symlink", "staging_file", "canceled"} {
		t.Run(kind, func(t *testing.T) { testStateStagingRefusal(t, kind) })
	}
}

func testStateStagingRefusal(t *testing.T, kind string) {
	t.Helper()
	_, stateRoot := engineSourceFixture(t)
	path := filepath.Join(stateRoot, "staging")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var err error
	switch kind {
	case "parent_mode":
		err = os.Chmod(stateRoot, 0755) //nolint:gosec // Deliberately unsafe fixture mode.
	case "parent_symlink":
		alias := stateRoot + "-alias"
		err = os.Symlink(stateRoot, alias)
		t.Cleanup(func() { _ = os.Remove(alias) })
		stateRoot = alias
	case "staging_mode":
		err = os.Mkdir(path, 0755) //nolint:gosec // Deliberately unsafe staging mode fixture.
	case "staging_symlink":
		err = os.Symlink(t.TempDir(), path)
	case "staging_file":
		err = os.WriteFile(path, []byte("keep"), 0600) //nolint:gosec // Deliberately wrong fixture type.
	case "canceled":
		cancel()
	}
	if err != nil {
		t.Fatal(err)
	}
	if parent, err := prepareEngineStaging(ctx, stateRoot); err == nil || parent != nil {
		t.Fatal("unsafe staging root admitted", kind)
	}
}

func TestStateStagingRefusesForeignOwnedStateRootWithoutMutation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the root-owned system fixture is not foreign to root")
	}
	before, err := os.Lstat("/etc")
	if err != nil {
		t.Fatal(err)
	}
	if parent, err := prepareEngineStaging(context.Background(), "/etc"); err == nil || parent != nil {
		t.Fatal("foreign-owned system parent admitted")
	}
	after, err := os.Lstat("/etc")
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("foreign-owned root was changed", err)
	}
}

func TestStateStagingFailedConsumerRemovesNewParent(t *testing.T) {
	binding, stateRoot := engineSourceFixture(t)
	if copied, err := copyEngineStateSource(context.Background(), binding, stateRoot, "invalid", engineCopyLimits{Bytes: 1 << 20, Files: 128, Depth: 16}); err == nil || copied != nil {
		t.Fatal("invalid consumer was accepted")
	}
	if _, err := os.Lstat(filepath.Join(stateRoot, "staging")); !os.IsNotExist(err) {
		t.Fatal("failed consumer left its new parent", err)
	}
}

func TestStateStagingRetirementPreservesReplacement(t *testing.T) {
	_, stateRoot := engineSourceFixture(t)
	parent, err := prepareEngineStaging(context.Background(), stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateRoot, "staging")
	if err := os.Rename(path, path+"-owned"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "existing")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil { //nolint:gosec // Replacement owner's fixture content.
		t.Fatal(err)
	}
	if consumer, err := openEngineStagingConsumer(path, parent); err == nil || consumer != nil {
		t.Fatal("replacement parent supplied a consumer root")
	}
	if err := parent.close(); err == nil {
		t.Fatal("replacement parent was retired")
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" { //nolint:gosec // Replacement owner's synthetic fixture marker.
		t.Fatal("replacement content was changed", err)
	}
}

func TestPrivateStagingCreationReceiptComesFromMkdir(t *testing.T) {
	_, stateRoot := engineSourceFixture(t)
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := root.Lstat("staging"); !os.IsNotExist(err) {
		t.Fatal("fixture staging already exists")
	}
	if err := root.Mkdir("staging", 0700); err != nil {
		t.Fatal(err)
	}
	created, err := engineMakePrivateDirectory(root, "staging")
	if err != nil || created {
		t.Fatal("pre-Mkdir absence supplied creation ownership", err)
	}
	lease, err := prepareEngineStaging(context.Background(), stateRoot)
	if err != nil || lease.created {
		t.Fatal("another creator's parent became owned", err)
	}
	if err := lease.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("staging"); err != nil {
		t.Fatal("another creator's empty parent was removed", err)
	}
}
