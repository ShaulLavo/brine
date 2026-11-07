//go:build linux

package store

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

type capturePairChangeContext struct {
	context.Context
	checks  atomic.Int64
	actions map[int64]func()
}

func (c *capturePairChangeContext) Err() error {
	if action := c.actions[c.checks.Add(1)]; action != nil {
		action()
	}
	return c.Context.Err()
}

func TestControlCaptureRefusesLateDBCreation(t *testing.T) {
	dir := stateDir(t)
	path := filepath.Join(dir, "control.db")
	ctx := &capturePairChangeContext{Context: context.Background()}
	ctx.actions = map[int64]func(){5: func() { writeCaptureFixture(t, path, []byte("created during final WAL read")) }}
	captured, err := CaptureBytes(ctx, dir, os.Geteuid())
	if err == nil {
		t.Fatalf("accepted Absent=%t after creating control.db during the second WAL lookup, checks=%d", captured.Absent, ctx.checks.Load())
	}
}

func TestControlCaptureRefusesNonexistentPair(t *testing.T) {
	dir := stateDir(t)
	db := filepath.Join(dir, "control.db")
	wal := db + "-wal"
	writeCaptureFixture(t, db, []byte("A"))
	writeCaptureFixture(t, wal, []byte("B"))
	rotate := func() {
		writeCaptureFixture(t, db, []byte("C"))
		writeCaptureFixture(t, wal, []byte("D"))
	}
	reset := func() {
		writeCaptureFixture(t, wal, []byte("B"))
		writeCaptureFixture(t, db, []byte("A"))
	}
	ctx := &capturePairChangeContext{Context: context.Background(), actions: map[int64]func(){7: rotate, 12: reset, 17: rotate}}
	captured, err := CaptureBytes(ctx, dir, os.Geteuid())
	if err == nil {
		t.Fatalf("accepted DB=%q WAL=%q, only A/B, C/B and C/D existed, checks=%d", captured.DB, captured.WAL, ctx.checks.Load())
	}
}
