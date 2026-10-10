//go:build linux

package replication

import (
	"bufio"
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLifetimeLockExcludesSecondReplicator(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	file := filepath.Join(dir, "binding.lock")
	first, err := AcquireLifetimeLock(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := AcquireLifetimeLock(context.Background(), file); err == nil {
		second.Release()
		t.Fatal("second replicator acquired lifetime lock")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("lock not private")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := first.Release(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	second, err := AcquireLifetimeLock(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	after, err := os.Stat(file)
	if err != nil || !os.SameFile(info, after) {
		t.Fatal("lifetime lock was replaced")
	}
}
func TestLifetimeLockRefusesSymlinkAndModeDrift(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	file := filepath.Join(dir, "binding.lock")
	if err := os.WriteFile(file, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLifetimeLock(context.Background(), file); err == nil {
		l.Release()
		t.Fatal("repaired unsafe lock")
	}
	link := filepath.Join(dir, "link.lock")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLifetimeLock(context.Background(), link); err == nil {
		l.Release()
		t.Fatal("accepted symlink")
	}
	parentLink := filepath.Join(dir, "parent")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if l, err := AcquireLifetimeLock(context.Background(), filepath.Join(parentLink, "other.lock")); err == nil {
		l.Release()
		t.Fatal("accepted symlink ancestor")
	}
}
func TestLifetimeLockRefusesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l, err := AcquireLifetimeLock(ctx, filepath.Join(t.TempDir(), "binding.lock")); err == nil {
		l.Release()
		t.Fatal("ignored cancellation")
	}
}

func TestLifetimeLockSurvivesExec(t *testing.T) {
	if file := os.Getenv("BRINE_TEST_EXEC_LOCK"); file != "" {
		lock, err := AcquireLifetimeLock(context.Background(), file)
		if err != nil {
			os.Exit(21)
		}
		other, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
		if err != nil {
			os.Exit(25)
		}
		if lock.inherit() != nil {
			os.Exit(22)
		}
		if unix.Exec("/bin/sh", []string{"sh", "-c", fmt.Sprintf("test ! -e /proc/self/fd/%d || exit 24; printf 'exec-ready\\n'; read ignored", other)}, []string{"PATH=/usr/bin:/bin"}) != nil {
			os.Exit(23)
		}
		return
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "binding.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLifetimeLockSurvivesExec$")
	cmd.Env = append(os.Environ(), "BRINE_TEST_EXEC_LOCK="+file)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "exec-ready\n" {
		t.Fatalf("exec failed %q %v", line, err)
	}
	if second, err := AcquireLifetimeLock(context.Background(), file); err == nil {
		second.Release()
		t.Fatal("exec released lifetime lock")
	}
	if _, err = stdin.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	stdin.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLifetimeLock(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}
