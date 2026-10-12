//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestNativeBackupUnitInstallationKeepsOwnershipAndExactCadence(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil { //nolint:gosec // Private directories require traversal permission.
		t.Fatal(err)
	}
	root, err := engineOpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := engineMakePrivateDirectories(root, ".config/systemd/user"); err != nil {
		t.Fatal(err)
	}
	metadata := store.NativeBackupActivationMetadata{Plan: store.NativeBackupRevisionPlan{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), Backup: policy.SpacetimePolicy{CaptureIntervalSeconds: 37}}}
	unit, timer := nativeBackupUnitBytes(metadata)
	if !strings.Contains(string(timer), "OnUnitActiveSec=37s") || !strings.Contains(string(unit), "host native-capture "+string(metadata.Plan.BindingID)) {
		t.Fatal("fixed scheduler changed binding or cadence")
	}
	name := nativeBackupUnitName(metadata) + ".timer"
	if err := replaceNativeBackupUnit(context.Background(), root, name, timer, ""); err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeBackupUnit(context.Background(), root, name, timer); err != nil {
		t.Fatal(err)
	}
	if err := replaceNativeBackupUnit(context.Background(), root, name, timer, ""); err == nil {
		t.Fatal("first activation adopted existing unowned timer")
	}
	replacement := []byte(strings.ReplaceAll(string(timer), "37s", "41s"))
	if err := replaceNativeBackupUnit(context.Background(), root, name, replacement, nativeBackupBytesHash(timer)); err != nil {
		t.Fatal("owned exact predecessor refused", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config/systemd/user", name), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceNativeBackupUnit(context.Background(), root, name, timer, nativeBackupBytesHash(replacement)); err == nil {
		t.Fatal("foreign timer was overwritten")
	}
}

func TestNativeBackupTimerReadbackRequiresEveryFixedProperty(t *testing.T) {
	expected := map[string]string{"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled", "FragmentPath": "/private/timer"}
	exact := "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nFragmentPath=/private/timer\n"
	if err := checkNativeBackupTimerProperties(exact, expected); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(exact, "active", "inactive", 1), strings.Replace(exact, "UnitFileState=enabled\n", "", 1), exact + "ActiveState=active\n", exact + "Unexpected=unknown\n"} {
		if checkNativeBackupTimerProperties(bad, expected) == nil {
			t.Fatal("uncertain timer readback activated backup policy")
		}
	}
}
