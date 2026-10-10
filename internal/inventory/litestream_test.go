package inventory

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/target"
)

type toolRunner struct {
	t     *testing.T
	value string
	err   error
}

func (r toolRunner) CaptureStdout(_ context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	r.t.Helper()
	if limit != 128 || path != LitestreamPath || len(args) != 1 || args[0] != "version" {
		r.t.Fatal("PATH or invalid tool lookup")
	}
	return localexec.Capture{Stdout: r.value}, r.err
}
func TestLitestreamObservation(t *testing.T) {
	checksum := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, output   string
		hashErr, error error
		status         target.Status
	}{
		{"known", "0.5.17\n", nil, nil, target.KnownStatus},
		{"absent", "", os.ErrNotExist, nil, target.Absent},
		{"unreadable", "", os.ErrPermission, nil, target.Unknown},
		{"wrong version", "0.5.18", nil, nil, target.Unknown},
		{"failed version", "0.5.17", nil, errors.New("probe failed"), target.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := LitestreamCollector{Runner: toolRunner{t, tc.output, tc.error}, HashExecutable: func(context.Context, string) (string, error) { return checksum, tc.hashErr }}
			got := c.Collect(context.Background())
			if got.Status != tc.status {
				t.Fatal(got)
			}
			if got.Value != nil && got.Value.ExecutableHash != checksum {
				t.Fatal(got)
			}
		})
	}
}
func TestLitestreamHashDrift(t *testing.T) {
	n := 0
	c := LitestreamCollector{Runner: toolRunner{t, "0.5.17", nil}, HashExecutable: func(context.Context, string) (string, error) { n++; return strings.Repeat("a", n), nil }}
	if got := c.Collect(context.Background()); got.Status != target.Unknown {
		t.Fatal(got)
	}
}

type overflowingToolRunner struct{ toolRunner }

func (r overflowingToolRunner) CaptureStdout(ctx context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	output, err := r.toolRunner.CaptureStdout(ctx, limit, path, args...)
	output.Overflow = true
	return output, err
}

func TestLitestreamCaptureOverflowRefused(t *testing.T) {
	c := LitestreamCollector{
		Runner:         overflowingToolRunner{toolRunner{t, "0.5.17", nil}},
		HashExecutable: func(context.Context, string) (string, error) { return "sha256:" + strings.Repeat("a", 64), nil },
	}
	if got := c.Collect(context.Background()); got.Status != target.Unknown {
		t.Fatal("accepted overflowed version capture", got)
	}
}
