//go:build linux

package store

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestPreparationCompletionTransitionRefreshIsWritableOnly(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, "DELETE FROM transitions WHERE kind=? AND from_state=? AND to_state=?", ops.Deploy, ops.Preparing, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	var count int
	query := "SELECT count(*) FROM transitions WHERE kind=? AND from_state=? AND to_state=?"
	if err = s.db.QueryRowContext(ctx, query, ops.Deploy, ops.Preparing, ops.Succeeded).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read-only open changed transitions: %d %v", count, err)
	}
	dir := s.dir
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if err = writer.db.QueryRowContext(ctx, query, ops.Deploy, ops.Preparing, ops.Succeeded).Scan(&count); err != nil || count != 1 {
		t.Fatalf("writable open did not refresh transitions: %d %v", count, err)
	}
}
