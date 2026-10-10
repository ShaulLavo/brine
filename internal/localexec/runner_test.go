package localexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunnerHelper(t *testing.T) {
	if !slices.Contains(os.Args, "BRINE_RUNNER_HELPER=1") {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "output":
		fmt.Fprint(os.Stdout, strings.Repeat("x", OutputLimit*8))
		fmt.Fprint(os.Stderr, "ignored excess stderr")
	case "stderr":
		fmt.Fprint(os.Stderr, "OpenSSH_9.9p2\n")
	case "exit":
		fmt.Fprint(os.Stderr, "synthetic-secret")
		os.Exit(7)
	case "timeout":
		time.Sleep(10 * time.Second)
	case "large-warning":
		fmt.Fprint(os.Stdout, `{"ok":true}`)
		fmt.Fprint(os.Stderr, strings.Repeat("warning", OutputLimit))
	default:
		mode := os.Args[len(os.Args)-1]
		if size, ok := strings.CutPrefix(mode, "capture-"); ok {
			n, err := strconv.Atoi(size)
			if err != nil {
				os.Exit(2)
			}
			fmt.Fprint(os.Stdout, strings.Repeat("x", n))
		} else {
			fmt.Fprint(os.Stdout, mode)
		}
	}
	os.Exit(0)
}

func TestExecRunner(t *testing.T) {
	t.Setenv("BRINE_RUNNER_HELPER", "1")
	for _, tt := range []struct {
		mode, want string
		wantError  bool
	}{
		{"output", strings.Repeat("x", OutputLimit), false},
		{"stderr", "OpenSSH_9.9p2\n", false},
		{"exit", "synthetic-secret", true},
		{"literal argument; $(not-a-command)", "literal argument; $(not-a-command)", false},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			output, err := (ExecRunner{}).Run(ctx, os.Args[0], "-test.run=^TestRunnerHelper$", "--", "BRINE_RUNNER_HELPER=1", tt.mode)
			want := tt.want
			if tt.mode != "output" {
				want += helperCoverageWarning()
			}
			if output != want || (err != nil) != tt.wantError {
				t.Fatalf("output = %q, error = %v", output, err)
			}
		})
	}
}

func TestExecRunnerTimeout(t *testing.T) {
	t.Setenv("BRINE_RUNNER_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (ExecRunner{}).Run(ctx, os.Args[0], "-test.run=^TestRunnerHelper$", "--", "BRINE_RUNNER_HELPER=1", "timeout")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("probe did not stop promptly")
	}
}

func TestBoundedOutputConcurrent(t *testing.T) {
	output := &boundedOutput{limit: OutputLimit}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			p := []byte(strings.Repeat("x", OutputLimit))
			for range 8 {
				if n, err := output.Write(p); n != len(p) || err != nil {
					t.Errorf("write = %d, %v", n, err)
				}
				if len(output.snapshot().Stdout) > OutputLimit {
					t.Error("output exceeds limit")
				}
			}
		})
	}
	wg.Wait()
	if len(output.snapshot().Stdout) != OutputLimit {
		t.Fatalf("output size = %d", len(output.snapshot().Stdout))
	}
}

func TestStdoutProbeSeparatesWarnings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	stdout, e := (ExecRunner{}).RunStdout(context.Background(), "sh", "-c", `printf '%s' '{"ok":true}'; printf '%s\n' 'warning' >&2`)
	if e != nil {
		t.Fatal(e)
	}
	if stdout != `{"ok":true}` {
		t.Fatalf("diagnostics entered stdout: %q", stdout)
	}
}

func TestCaptureStdoutBoundaries(t *testing.T) {
	for _, limit := range []int{OutputLimit, 256 << 10} {
		for _, size := range []int{limit - 1, limit, limit + 1} {
			t.Run(fmt.Sprintf("%d/%d", limit, size), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				out, err := (ExecRunner{}).CaptureStdout(ctx, limit, os.Args[0], "-test.run=^TestRunnerHelper$", "--", "BRINE_RUNNER_HELPER=1", fmt.Sprintf("capture-%d", size))
				if err != nil || len(out.Stdout) != min(size, limit) || out.Overflow != (size > limit) || out.Stdout != strings.Repeat("x", min(size, limit)) {
					t.Fatalf("size=%d limit=%d: capture bytes=%d overflow=%t error=%v", size, limit, len(out.Stdout), out.Overflow, err)
				}
			})
		}
	}
}

func TestBoundedOutputSplitWrites(t *testing.T) {
	out := &boundedOutput{limit: 4}
	for _, chunk := range []string{"abc", "d", ""} {
		if n, err := out.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("write=%d error=%v", n, err)
		}
	}
	if got := out.snapshot(); got.Stdout != "abcd" || got.Overflow {
		t.Fatalf("exact capture=%+v", got)
	}
	if n, err := out.Write([]byte("e")); n != 1 || err != nil {
		t.Fatalf("overflow write=%d error=%v", n, err)
	}
	if got := out.snapshot(); got.Stdout != "abcd" || !got.Overflow {
		t.Fatalf("overflow capture=%+v", got)
	}
}

func TestCaptureStdoutSeparatesDiagnosticOverflow(t *testing.T) {
	out, err := (ExecRunner{}).CaptureStdout(context.Background(), OutputLimit, os.Args[0], "-test.run=^TestRunnerHelper$", "--", "BRINE_RUNNER_HELPER=1", "large-warning")
	if err != nil || out.Stdout != `{"ok":true}` || out.Overflow {
		t.Fatalf("capture=%+v error=%v", out, err)
	}
}

func TestCaptureStdoutRejectsInvalidLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		if _, err := (ExecRunner{}).CaptureStdout(context.Background(), limit, "unused"); !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("limit=%d error=%v", limit, err)
		}
	}
}
