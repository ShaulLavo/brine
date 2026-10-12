//go:build linux

package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestEngineFenceSubprocess(t *testing.T) {
	root := os.Getenv("BRINE_ENGINE_FENCE_ROOT")
	if root == "" {
		return
	}
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	fmt.Println("trying")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = s.HoldDataFence(ctx, data.DatabaseID(strings.Repeat("a", 32)), "child-fence"); err != nil {
		t.Fatal(err)
	}
	fmt.Println("committed")
}

func TestEngineFenceSecondProcessWaitsUntilHandoff(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r, err := s.ReserveEngine(ctx, engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, s.dir, r.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	launch, err := data.AcquireEngineLaunchGuard(ctx, s.dir, r.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = launch.Release() }()
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, s.dir, r.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lifetime.Release() }()
	process, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(process, os.Args[0], "-test.run=^TestEngineFenceSubprocess$") //nolint:gosec // Reexecute this test binary for kernel flock exclusion.
	command.Env = append(os.Environ(), "BRINE_ENGINE_FENCE_ROOT="+s.dir)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "trying" {
		t.Fatal("child never reached guarded fence claim", scanner.Err())
	}
	committed := make(chan bool, 1)
	go func() { committed <- scanner.Scan() && scanner.Text() == "committed" }()
	select {
	case <-committed:
		t.Fatal("fence committed before native handoff")
	case <-time.After(75 * time.Millisecond):
	}
	if err = launch.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-committed:
		if !ok {
			t.Fatal("fence did not progress after handoff")
		}
	case <-process.Done():
		t.Fatal("lifetime blocked fence claiming")
	}
	if err = command.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err = data.AcquireEngineLifetimeLock(ctx, s.dir, r.Database.BindingID); !errors.Is(err, data.ErrEngineLocked) {
		t.Fatal("handoff lost lifetime lock", err)
	}
	var held int
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM data_fences WHERE database_id=? AND state='held'", r.Database.DatabaseID).Scan(&held); err != nil || held != 1 {
		t.Fatal("second-process fence not durable", err, held)
	}
}
