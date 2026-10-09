//go:build linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchLockSecurityExclusionAndIndependence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	launch, err := s.AcquireLaunchLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Release()
	info, err := os.Stat(filepath.Join(s.dir, "launch.lock"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private lock mode: %v %v", info, err)
	}
	host, err := s.AcquireHostLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
	wait, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := s.AcquireLaunchLock(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("launch exclusion: %v", err)
	}
	launch.Release()
	reacquired, err := s.AcquireLaunchLock(ctx)
	if err != nil {
		t.Fatalf("host lock blocks independent launch: %v", err)
	}
	reacquired.Release()
}

func TestLaunchLockRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s := openTest(t)
			path := filepath.Join(s.dir, "launch.lock")
			var err error
			if kind == "symlink" {
				err = os.Symlink(filepath.Join(s.dir, "brine.db"), path)
			} else {
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if lock, err := s.AcquireLaunchLock(context.Background()); err == nil {
				lock.Release()
				t.Fatal("unsafe lock accepted")
			}
		})
	}
}

func TestLaunchLockExclusionAndSIGKILLRecovery(t *testing.T) {
	dir := stateDir(t)
	cmd := startHelper(t, dir, "", "launch-lock")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wait, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = s.AcquireLaunchLock(wait)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live child did not exclude launcher: %v", err)
	}
	killHelper(t, cmd)
	wait, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := s.AcquireLaunchLock(wait)
	if err != nil {
		t.Fatalf("SIGKILL left launch lock held: %v", err)
	}
	lock.Release()
}
