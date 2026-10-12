//go:build linux

package data

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineLocksRequireCurrentRootInodeAndHeldDescriptor(t *testing.T) {
	for _, role := range []string{"launch", "lifetime"} {
		t.Run(role, func(t *testing.T) { checkEngineLockScope(t, role) })
	}
}

func checkEngineLockScope(t *testing.T, role string) {
	t.Helper()
	root, foreign := t.TempDir(), t.TempDir()
	binding := ReplicaBindingID(strings.Repeat("a", 32))
	ctx := context.Background()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareEngineLocks(ctx, root, binding); err != nil {
		t.Fatal(err)
	}
	acquire, holds := AcquireEngineLaunchGuard, (*EngineLock).HoldsLaunch
	if role == "lifetime" {
		acquire, holds = AcquireEngineLifetimeLock, (*EngineLock).HoldsLifetime
	}
	lock, err := acquire(ctx, root, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if !holds(lock, root, binding) || holds(lock, foreign, binding) || holds(lock, "", binding) {
		t.Fatal("lock scope accepted a foreign root or rejected its actual root")
	}
	path := filepath.Join(root, "engine-locks", string(binding)+"."+role)
	if err = os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if holds(lock, root, binding) {
		t.Fatal("replaced lock inode admitted held authority")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(path+".original", path); err != nil {
		t.Fatal(err)
	}
	if !holds(lock, root, binding) {
		t.Fatal("restored original lock was not current")
	}
	if err = lock.Release(); err != nil || holds(lock, root, binding) {
		t.Fatal("released lock admitted authority", err)
	}
}
