package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func managedUnitsFixture(t *testing.T) (*managedUnits, string, managedServiceChange) {
	t.Helper()
	// A world-writable temporary ancestor is deliberately refused in production.
	// Keep this artifact fixture under the owned checkout and remove it afterwards.
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp(directory, ".managed-units-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.Chmod(home, 0700); err != nil { // #nosec G302 -- private test directory requires traversal.
		t.Fatal(err)
	}
	for _, path := range []string{".config", ".config/systemd", managedServiceDirectory} {
		if err := os.Mkdir(filepath.Join(home, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	units, err := openManagedUnits(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = units.Close() })
	binding := data.ReplicaBindingID(strings.Repeat("a", 32))
	raw := []byte("# Brine-owned engine=" + string(binding) + "\n[Service]\nType=notify\n")
	change := managedServiceChange{binding: binding, name: "brine-engine-" + string(binding) + ".service", raw: raw, wanted: managedServiceHash(raw)}
	return units, home, change
}

func TestManagedUnitsInstallPreservesOwnedPredecessor(t *testing.T) {
	units, _, first := managedUnitsFixture(t)
	ctx := context.Background()
	if err := units.stage(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := units.install(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.previous = first.wanted
	second.raw = append([]byte{}, first.raw...)
	second.raw = append(second.raw, []byte("RestartSec=5s\n")...)
	second.wanted = managedServiceHash(second.raw)
	if err := units.stage(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := units.install(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := units.verify(ctx, second, second.wanted); err != nil {
		t.Fatal(err)
	}
	retained, exists, err := units.read(ctx, filepath.Join(managedServiceStaging, first.name+"."+first.wanted))
	if err != nil || !exists || string(retained) != string(first.raw) {
		t.Fatal("owned predecessor not retained", err)
	}

}

func TestManagedUnitsPrivateDirectoryUnderTraversableHome(t *testing.T) {
	units, home, change := managedUnitsFixture(t)
	for _, path := range []string{home, filepath.Join(home, ".config"), filepath.Join(home, ".config/systemd")} {
		if err := os.Chmod(path, 0755); err != nil { //nolint:gosec // Owned traversable ancestor fixture.
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := units.stage(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err := units.install(ctx, change); err != nil {
		t.Fatal(err)
	}
	if err := units.verify(ctx, change, change.wanted); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, managedServiceDirectory), 0755); err != nil { //nolint:gosec // Unsafe private directory fixture.
		t.Fatal(err)
	}
	if units.verify(ctx, change, change.wanted) == nil {
		t.Fatal("public unit directory accepted")
	}
}

func TestManagedUnitsRefuseWritableHomeAncestor(t *testing.T) {
	_, home, _ := managedUnitsFixture(t)
	if err := os.Chmod(home, 0770); err != nil { //nolint:gosec // Deliberately writable ancestor fixture.
		t.Fatal(err)
	}
	if units, err := openManagedUnits(home); err == nil {
		_ = units.Close()
		t.Fatal("writable home accepted")
	}
}

func TestManagedUnitsRefuseUnownedActiveStagedAndRetainedFiles(t *testing.T) {
	for _, role := range []string{"active", "staged", "retained"} {
		t.Run(role, func(t *testing.T) { checkManagedUnownedFile(t, role) })
	}
}

func checkManagedUnownedFile(t *testing.T, role string) {
	t.Helper()
	units, home, change := managedUnitsFixture(t)
	ctx := context.Background()
	if err := units.stage(ctx, change); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, managedServiceDirectory, change.name)
	if role == "staged" {
		path = filepath.Join(home, managedServiceStaging, change.name+"."+change.wanted)
	}
	if role == "retained" {
		if err := units.install(ctx, change); err != nil {
			t.Fatal(err)
		}
		change.previous = change.wanted
		change.raw = append(append([]byte{}, change.raw...), []byte("RestartSec=5s\n")...)
		change.wanted = managedServiceHash(change.raw)
		if err := units.stage(ctx, change); err != nil {
			t.Fatal(err)
		}
		path = filepath.Join(home, managedServiceStaging, change.name+"."+change.previous)
	}
	if err := os.WriteFile(path, []byte("foreign bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := units.install(ctx, change); err == nil {
		t.Fatal("unowned artifact accepted")
	}
}

func TestManagedUnitsRefuseLinkModeAndBindingEscape(t *testing.T) {
	units, home, change := managedUnitsFixture(t)
	ctx := context.Background()
	if err := units.stage(ctx, change); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, managedServiceDirectory, change.name)
	outside := filepath.Join(home, "outside")
	if err := os.WriteFile(outside, []byte("outside unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := units.install(ctx, change); err == nil {
		t.Fatal("symlink accepted")
	}
	raw, err := os.ReadFile(outside) // #nosec G304 -- test-owned sentinel proves symlink targets stay unchanged.
	if err != nil || string(raw) != "outside unchanged" {
		t.Fatal("outside target changed")
	}
	change.name = "../outside"
	if err := units.stage(ctx, change); err == nil {
		t.Fatal("caller-selected path accepted")
	}
}
