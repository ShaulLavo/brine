//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fmt"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
	"math"
	"time"
)

func TestNativeBackupUnitInstallationKeepsOwnershipAndExactCadence(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil { //nolint:gosec // Private directories require traversal permission.
		t.Fatal(err)
	}
	config := filepath.Join(home, ".config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := engineOpenRoot(config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := engineMakePrivateDirectories(root, "systemd/user"); err != nil {
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

type nativeBackupManagerFixture struct{ home string }

func (r nativeBackupManagerFixture) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path != "/usr/bin/systemctl" {
		return localexec.Result{}, fmt.Errorf("unexpected backup command")
	}
	if c.Mutation {
		return localexec.Result{}, nil
	}
	name := c.Args[2]
	return localexec.Result{Stdout: "LoadState=loaded\nActiveState=active\nUnitFileState=enabled\nFragmentPath=" + filepath.Join(r.home, ".config/systemd/user", name) + "\n"}, nil
}

func TestNativeBackupInstallUsesPrivateConfigUnderTraversableHome(t *testing.T) {
	_, home, _ := managedUnitsFixture(t)
	if err := os.Chmod(home, 0755); err != nil { //nolint:gosec // Enrolled homes are traversable, while configuration stays private.
		t.Fatal(err)
	}
	session, err := localexec.NewSession(nativeBackupManagerFixture{home}, nativeBackupFixtureUID(t), home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	metadata := store.NativeBackupActivationMetadata{Plan: store.NativeBackupRevisionPlan{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), Backup: policy.SpacetimePolicy{CaptureIntervalSeconds: 37}}}
	unit, timer := nativeBackupUnitBytes(metadata)
	if err = installNativeBackupUnits(context.Background(), home, session, metadata, unit, timer); err != nil {
		t.Fatal("protected traversable home refused", err)
	}
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("home changed", err)
	}
}

func TestNativeBackupInstallCreatesOnlyPrivateConfigDescendants(t *testing.T) {
	_, home, _ := managedUnitsFixture(t)
	if err := os.Remove(filepath.Join(home, managedServiceDirectory)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, ".config/systemd")); err != nil {
		t.Fatal(err)
	}
	session, err := localexec.NewSession(nativeBackupManagerFixture{home}, nativeBackupFixtureUID(t), home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	metadata := store.NativeBackupActivationMetadata{Plan: store.NativeBackupRevisionPlan{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), Backup: policy.SpacetimePolicy{CaptureIntervalSeconds: 37}}}
	unit, timer := nativeBackupUnitBytes(metadata)
	if err = installNativeBackupUnits(context.Background(), home, session, metadata, unit, timer); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{".config", ".config/systemd", managedServiceDirectory} {
		info, err := os.Lstat(filepath.Join(home, relative))
		if err != nil || !managedServiceNode(info, true) {
			t.Fatal("new unit boundary is not private", relative, err)
		}
	}
}

func nativeBackupFixtureUID(t *testing.T) uint32 {
	t.Helper()
	value := int64(os.Geteuid())
	if value < 0 || value >= math.MaxUint32 {
		t.Fatal("fixture UID outside bounded range")
		return 0
	}
	return uint32(value)
}
