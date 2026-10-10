//go:build linux

package enroll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type captureProbeExecutor struct {
	result localexec.Result
	err    error
}

func (e captureProbeExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Mutation || len(c.Stdin) != 0 {
		return localexec.Result{}, errors.New("probe must be read-only")
	}
	return e.result, e.err
}

func TestEnrollmentCaptureBounds(t *testing.T) {
	for _, limit := range []int{4096, localexec.CommandOutputLimit} {
		for _, size := range []int{limit - 1, limit, limit + 1} {
			t.Run(fmt.Sprintf("%d/%d", limit, size), func(t *testing.T) {
				r := probeRunner{executor: captureProbeExecutor{result: localexec.Result{Stdout: strings.Repeat("x", size)}}}
				out, err := r.CaptureStdout(context.Background(), limit, "fixture")
				if err != nil || len(out.Stdout) != min(size, limit) || out.Overflow != (size > limit) {
					t.Fatalf("capture bytes=%d overflow=%t error=%v", len(out.Stdout), out.Overflow, err)
				}
			})
		}
	}
	r := probeRunner{executor: captureProbeExecutor{result: localexec.Result{Stdout: "valid prefix", Truncated: true}}}
	out, err := r.CaptureStdout(context.Background(), 4096, "fixture")
	if err != nil || !out.Overflow {
		t.Fatal("executor truncation must not become complete evidence")
	}
	failure := errors.New("fixture failure")
	r.executor = captureProbeExecutor{err: failure}
	if _, err := r.CaptureStdout(context.Background(), 4096, "fixture"); !errors.Is(err, failure) {
		t.Fatal("executor error lost")
	}
	for _, limit := range []int{0, -1} {
		if _, err := r.CaptureStdout(context.Background(), limit, "fixture"); !errors.Is(err, localexec.ErrOutputLimit) {
			t.Fatal("invalid capture limit accepted")
		}
	}
}
