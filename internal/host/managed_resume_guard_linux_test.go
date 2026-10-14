//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func TestManagedFrontendPreStartCannotBootstrapEngineDependency(t *testing.T) {
	home := t.TempDir()
	rig, planned, _, _ := managedFactsRecoveryFixture(t, func(runtime *data.EngineRuntime) {
		raw, err := EngineServiceBytes(*runtime)
		if err != nil {
			t.Fatal(err)
		}
		runtime.UnitSHA256 = managedServiceHash(raw)
		directory := filepath.Join(home, ".config/systemd/user")
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, engineContainerName(runtime.BindingID)+".service"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	})
	shows := 0
	units := &systemd.Fake{JobPendingFunc: func(_ context.Context, _ systemd.Unit) (bool, error) { return false, nil }, ShowFunc: func(_ context.Context, _ systemd.Unit) (systemd.Properties, error) {
		shows++
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}}
	runtime := managedRuntime{state: rig.store, home: home, observer: managedEngineObserver{state: rig.store, home: home, units: units}}
	_, desired, err := rig.store.LoadPlan(t.Context(), planned.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.BeforeFrontendStart(t.Context(), "reviewed-start", planned, desired, "unchanged-unit"); err == nil {
		t.Fatal("inactive engine granted pre-start frontend authority")
	}
	if shows != 1 {
		t.Fatal("guard did not reach actual inactive engine observation", shows)
	}
}
