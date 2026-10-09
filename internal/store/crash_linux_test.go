//go:build linux

package store

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
)

func startHelper(t *testing.T, dir, op, stage string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreCrashHelper$")
	cmd.Env = append(os.Environ(), "BRINE_STORE_CHILD=1", "BRINE_STORE_DIR="+dir, "BRINE_STORE_OP="+op, "BRINE_STORE_STAGE="+stage)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	ready := make(chan error, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		if s.Scan() && s.Text() == "ready" {
			ready <- nil
		} else {
			ready <- fmt.Errorf("child failed before barrier: %s", stderr.String())
		}
	}()
	select {
	case e = <-ready:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal("child barrier timeout")
	}
	return cmd
}
func killHelper(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	if e := cmd.Wait(); e == nil {
		t.Fatal("child was not killed")
	}
}
func TestStoreCrashHelper(t *testing.T) {
	if os.Getenv("BRINE_STORE_CHILD") != "1" {
		return
	}
	s, e := Open(os.Getenv("BRINE_STORE_DIR"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	op := os.Getenv("BRINE_STORE_OP")
	switch os.Getenv("BRINE_STORE_STAGE") {
	case "lock":
		lock, e := s.AcquireHostLock(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer lock.Release()
	case "event":
		if _, e = s.AppendEvent(ctx, op, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)}); e != nil {
			t.Fatal(e)
		}
	case "state":
		if e = s.SetOperationState(ctx, op, ops.Preflight); e != nil {
			t.Fatal(e)
		}
	case "state-uncommitted":
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, "UPDATE operations SET state='preflight',updated_at=? WHERE id=?", timestamp(), op); e != nil {
			t.Fatal(e)
		}
		if _, e = appendEvent(ctx, tx, op, Event{Kind: "state", State: ops.Preflight}); e != nil {
			t.Fatal(e)
		}
	case "release":
		r := release(t, s, "release-0002")
		if e = s.CommitRelease(ctx, "hello", r); e != nil {
			t.Fatal(e)
		}
	case "release-uncommitted":
		r := release(t, s, "release-0002")
		raw, _ := json.Marshal(r)
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, "INSERT INTO releases VALUES(?,?,?,?,?,?)", "hello", r.ID, r.PlanID, raw, digest(raw), timestamp()); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, "UPDATE release_heads SET previous_id=current_id,current_id=? WHERE app='hello'", r.ID); e != nil {
			t.Fatal(e)
		}
	default:
		t.Fatal("unknown child stage")
	}
	fmt.Println("ready")
	select {}
}
func TestSIGKILLJournalConsistency(t *testing.T) {
	for _, stage := range []string{"event", "state", "state-uncommitted", "release", "release-uncommitted"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			dir := stateDir(t)
			s, e := Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			op := operation(t, s)
			r := release(t, s, "release-0001")
			if e = s.CommitRelease(ctx, "hello", r); e != nil {
				t.Fatal(e)
			}
			s.Close()
			cmd := startHelper(t, dir, op, stage)
			killHelper(t, cmd)
			s, e = Open(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			var integrity string
			if e = s.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); e != nil || integrity != "ok" {
				t.Fatalf("database integrity %s %v", integrity, e)
			}
			current, e := s.CurrentRelease(ctx, "hello")
			if e != nil {
				t.Fatal(e)
			}
			wantID := "release-0001"
			if stage == "release" {
				wantID = "release-0002"
			}
			if current.ID != wantID {
				t.Fatalf("release head %s want %s", current.ID, wantID)
			}
			operation, e := s.GetOperation(ctx, op)
			if e != nil {
				t.Fatal(e)
			}
			wantState := ops.Queued
			if stage == "state" {
				wantState = ops.Preflight
			}
			if operation.State != wantState {
				t.Fatalf("state %s want %s", operation.State, wantState)
			}
			events, e := s.EventsAfter(ctx, op, 0, 100)
			if e != nil {
				t.Fatal(e)
			}
			wantEvents := 0
			if stage == "event" || stage == "state" {
				wantEvents = 1
			}
			if len(events) != wantEvents {
				t.Fatalf("events %d want %d", len(events), wantEvents)
			}
		})
	}
}
func TestHostLockExclusionAndSIGKILLRecovery(t *testing.T) {
	dir := stateDir(t)
	cmd := startHelper(t, dir, "", "lock")
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if lock, e := s.AcquireHostLock(ctx); e == nil {
		lock.Release()
		t.Fatal("second process acquired lock")
	}
	killHelper(t, cmd)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	lock, e := s.AcquireHostLock(ctx2)
	if e != nil {
		t.Fatalf("stale holder recovery %v", e)
	}
	defer lock.Release()
	ctx3, cancel3 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel3()
	if l, e := s.AcquireHostLock(ctx3); e == nil {
		l.Release()
		t.Fatal("same process concurrent acquisition")
	}
	if e = lock.Release(); e != nil {
		t.Fatal(e)
	}
	if e = lock.Release(); e != nil {
		t.Fatal("release not idempotent")
	}
}
