package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestStartupRuntimeIsLazyNarrowAndClosesBeforeOutput(t *testing.T) {
	for _, args := range [][]string{{"host", "writer-attempt"}, {"host", "writer-attempt", "invalid"}, {"host", "replica-exec", "invalid", "invalid", "invalid", "invalid", "/bad", "/bad"}} {
		opens := 0
		deps := Dependencies{Context: context.Background(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		lifecycle := RuntimeLifecycle{OpenWriterAttempt: func(context.Context) (RuntimeServices, error) { opens++; return RuntimeServices{}, nil }, OpenPermits: func(context.Context) (RuntimeServices, error) { opens++; return RuntimeServices{}, nil }, Open: func(context.Context, bool) (RuntimeServices, error) {
			t.Fatal("broad runtime opened")
			return RuntimeServices{}, nil
		}}
		if ExecuteWithRuntime(deps, args, lifecycle) == nil || opens != 0 {
			t.Fatal("validation did not precede open", args, opens)
		}
	}
	var stdout bytes.Buffer
	opens, attempts, closes := 0, 0, 0
	deps := Dependencies{Context: context.Background(), Stdout: &stdout, Stderr: &bytes.Buffer{}}
	lifecycle := RuntimeLifecycle{OpenWriterAttempt: func(context.Context) (RuntimeServices, error) {
		opens++
		return RuntimeServices{WriterAttempt: func(context.Context, string) error { attempts++; return nil }, Close: func() error {
			closes++
			if stdout.Len() != 0 {
				t.Fatal("output before cleanup")
			}
			return errors.New("cleanup failed")
		}}, nil
	}, Open: func(context.Context, bool) (RuntimeServices, error) {
		t.Fatal("broad runtime opened")
		return RuntimeServices{}, nil
	}}
	if ExecuteWithRuntime(deps, []string{"--json", "host", "writer-attempt", strings.Repeat("4", 32)}, lifecycle) == nil {
		t.Fatal("cleanup failure reported success")
	}
	if opens != 1 || attempts != 1 || closes != 1 || strings.Contains(stdout.String(), `"ok":true`) {
		t.Fatal("wrong lifecycle", opens, attempts, closes, stdout.String())
	}
}
