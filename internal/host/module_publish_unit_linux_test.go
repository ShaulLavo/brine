//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInitialPublicationObservesAbsentServiceUntilOrdinaryDeploy(t *testing.T) {
	_, home, _ := managedUnitsFixture(t)
	permit, _ := engineStartupFixture(t)
	runtime := permit.Runtime
	raw, err := EngineServiceBytes(runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtime.UnitSHA256 = managedServiceHash(raw)
	ctx := context.Background()
	unit, err := readPublicationUnit(ctx, home, runtime, true)
	if err != nil || unit != "" {
		t.Fatal("fresh initial publication requires a deployed service", err)
	}
	if _, err = readPublicationUnit(ctx, home, runtime, false); err == nil {
		t.Fatal("initialized publication accepted a missing service")
	}
	name := filepath.Join(home, managedServiceDirectory, "brine-engine-"+string(runtime.BindingID)+".service")
	if _, err = os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("publication observation created a service", err)
	}
	if err = os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readPublicationUnit(ctx, home, runtime, true); err == nil {
		t.Fatal("initial publication accepted an unexpected installed service")
	}
	unit, err = readPublicationUnit(ctx, home, runtime, false)
	if err != nil || unit != runtime.UnitSHA256 {
		t.Fatal("initialized publication refused its exact installed service", err)
	}
	if err = os.WriteFile(name, []byte("foreign service"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readPublicationUnit(ctx, home, runtime, false); err == nil {
		t.Fatal("initialized publication accepted a foreign service")
	}
}

func TestInitialPublicationRefusesUnsafeUnitAndChangedServiceDeclaration(t *testing.T) {
	_, home, _ := managedUnitsFixture(t)
	permit, _ := engineStartupFixture(t)
	runtime := permit.Runtime
	raw, err := EngineServiceBytes(runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtime.UnitSHA256 = managedServiceHash(raw)
	ctx := context.Background()
	runtime.StopSeconds++
	if _, err = readPublicationUnit(ctx, home, runtime, true); err == nil {
		t.Fatal("initial publication accepted a changed service declaration")
	}
	runtime.StopSeconds--
	name := filepath.Join(home, managedServiceDirectory, "brine-engine-"+string(runtime.BindingID)+".service")
	if err = os.Symlink(filepath.Join(home, "absent"), name); err != nil {
		t.Fatal(err)
	}
	if _, err = readPublicationUnit(ctx, home, runtime, true); err == nil {
		t.Fatal("initial publication accepted a dangling service symlink")
	}
}
