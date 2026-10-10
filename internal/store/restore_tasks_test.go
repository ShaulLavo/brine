//go:build linux

package store

import (
	"context"
	"github.com/ShaulLavo/brine/internal/ops"
	"testing"
)

func TestRestoreTaskInputImmutableAndRequesterScoped(t *testing.T) {
	ctx := context.Background()
	state := openTest(t)
	input := ops.RestoreTaskInput{App: "example", Database: "audit", TXID: "7"}
	id, err := state.SaveRestoreTaskInput(ctx, "requester1", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.LoadRestoreTaskInput(ctx, id, "requester2"); err == nil {
		t.Fatal("restore reference crossed requester")
	}
	got, err := state.LoadRestoreTaskInput(ctx, id, "requester1")
	if err != nil || got != input {
		t.Fatal("restore selector lost", err)
	}
	if _, err := state.db.ExecContext(ctx, "UPDATE restore_task_inputs SET canonical='{}' WHERE id=?", id); err == nil {
		t.Fatal("restore input mutable")
	}
	readonly, err := OpenReadOnly(ctx, state.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readonly.Close() }()
	got, err = readonly.LoadRestoreTaskInput(ctx, id, "requester1")
	if err != nil || got != input {
		t.Fatal("restore input not durable", err)
	}
}
