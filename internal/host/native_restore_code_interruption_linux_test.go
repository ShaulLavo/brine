//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

func nativeRestoreCodeInterruptionFixture(t *testing.T, engineOnly bool) (string, store.NativeRestoreRecoveryCode, store.NativeRestoreTransitionView, *engineTransitionTree, []byte) {
	t.Helper()
	tree := engineTreeTransitionFixture(t)
	common := tree.input.Common()
	selected := common.PreparedPoint
	selected.Kind = data.EngineRestorePointEvidence
	input, err := data.NewEngineRestoreTransition(common, data.EngineRestoreTarget{SelectedPoint: selected})
	if err != nil {
		t.Fatal(err)
	}
	tree.input = input
	prepared, err := data.PrepareEngineTransition(input)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	manager, err := quadlet.NewManager(home, quadletValidator{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	source := []byte("# Brine-owned plan=sha256:" + strings.Repeat("a", 64) + "\n[Container]\nImage=source\n")
	replacement := []byte(strings.ReplaceAll(string(source), "Image=source", "Image=recovery"))
	unit := target.Unit{Name: "fixture.container", Hash: "sha256:" + managedServiceHash(source)}
	if err = os.WriteFile(filepath.Join(home, quadlet.ActiveDirectory, unit.Name), source, 0600); err != nil {
		t.Fatal(err)
	}
	recovery := store.NativeRestoreRecoveryCode{
		SourceRelease: store.Release{Managed: &ops.ManagedRelease{Frontend: &ops.ManagedFrontend{Unit: unit}}},
		Release:       store.Release{Managed: &ops.ManagedRelease{}},
		Plan:          plan.Plan{Managed: &plan.ManagedServicePlan{}},
	}
	if !engineOnly {
		recovery.Release.Managed.Frontend = &ops.ManagedFrontend{Unit: target.Unit{Name: unit.Name, Hash: "sha256:" + managedServiceHash(replacement)}}
	}
	view := store.NativeRestoreTransitionView{Input: input, Cursor: prepared.Cursor()}
	return home, recovery, view, tree, replacement
}

func TestNativeRestoreCodeInstallationInterruptionRetainsSourceContinuation(t *testing.T) {
	for _, engineOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("engine_only=%v", engineOnly), func(t *testing.T) {
			testNativeRestoreCodeInstallationInterruption(t, engineOnly)
		})
	}
}

func testNativeRestoreCodeInstallationInterruption(t *testing.T, engineOnly bool) {
	home, recovery, view, tree, replacement := nativeRestoreCodeInterruptionFixture(t, engineOnly)
	ctx := context.Background()
	pending := view
	pending.Cursor = data.EngineTransitionCursor{}
	if err := verifyNativeRestoreTransitionUnits(ctx, home, recovery, pending, tree); err != nil {
		t.Fatal("source frontend refused before transition admission", err)
	}
	if view.Cursor.Sequence != 1 || view.Cursor.Stage != data.EngineTransitionPrepared {
		t.Fatal("fixture did not stop after transition admission", view.Cursor)
	}
	if err := verifyNativeRestoreTransitionUnits(ctx, home, recovery, view, tree); err != nil {
		t.Fatal("transition admission stranded the untouched source frontend", err)
	}
	unit := recovery.SourceRelease.Managed.Frontend.Unit
	if engineOnly {
		manager, err := quadlet.NewManager(home, quadletValidator{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = manager.Close() }()
		if err = manager.RemoveRetained(ctx, unit.Name, unit.Hash); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(home, quadlet.ActiveDirectory, unit.Name), replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeRestoreTransitionUnits(ctx, home, recovery, view, tree); err != nil {
		t.Fatal("installed recovery frontend refused", err)
	}
	layout, err := tree.inspect(ctx)
	if err != nil || data.ClassifyEngineTransitionLayout(view.Input, layout) != data.EngineLayoutPrepared {
		t.Fatal("code recovery changed the original or staged tree", err)
	}
}

func TestNativeRestoreSourceContinuationRequiresPreparedLayoutAndGuards(t *testing.T) {
	for _, fault := range []string{"foreign frontend", "missing frontend", "changed source", "moved original", "past code installation", "released guard"} {
		t.Run(fault, func(t *testing.T) {
			testNativeRestoreSourceContinuationRefusal(t, fault)
		})
	}
}

func testNativeRestoreSourceContinuationRefusal(t *testing.T, fault string) {
	home, recovery, view, tree, _ := nativeRestoreCodeInterruptionFixture(t, false)
	ctx := context.Background()
	unit := recovery.SourceRelease.Managed.Frontend.Unit
	path := filepath.Join(home, quadlet.ActiveDirectory, unit.Name)
	var err error
	switch fault {
	case "foreign frontend":
		err = os.WriteFile(path, []byte("[Container]\nImage=foreign\n"), 0600)
	case "missing frontend":
		err = os.Remove(path)
	case "changed source":
		err = os.WriteFile(filepath.Join(string(tree.binding.Root), tree.binding.RelativeDirectory, "control-db/state"), []byte("changed"), 0600)
	case "moved original":
		intent := preserveIntentFixture(t, tree)
		err = tree.move(ctx, intent, enginePreserveOriginal)
	case "past code installation":
		view.Cursor = preserveIntentFixture(t, tree)
	case "released guard":
		err = tree.preparation.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeRestoreTransitionUnits(ctx, home, recovery, view, tree); err == nil {
		t.Fatal("unsafe source continuation admitted", fault)
	}
}
