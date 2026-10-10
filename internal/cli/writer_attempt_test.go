package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestWriterAttemptHasNarrowCompositionAndBoundedContext(t *testing.T) {
	args := []string{"host", "writer-attempt", strings.Repeat("4", 32)}
	if HostRuntimeRequested(args) {
		t.Fatal("attempt selects broad mutating runtime")
	}
	calls := 0
	deps := Dependencies{Context: context.Background(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, HostWriterAttempt: func(ctx context.Context, id string) error {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || id != args[2] {
			t.Fatal("unbounded or wrong attempt")
		}
		return nil
	}}
	root := NewRootCommand(deps)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("missing narrow attempt")
	}
	deps.HostWriterAttempt = nil
	root = NewRootCommand(deps)
	root.SetArgs(args)
	if root.Execute() == nil {
		t.Fatal("missing composition admitted")
	}
}
