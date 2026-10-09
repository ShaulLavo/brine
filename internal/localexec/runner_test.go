package localexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
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
	default:
		fmt.Fprint(os.Stdout, os.Args[len(os.Args)-1])
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
	output := &boundedOutput{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			p := []byte(strings.Repeat("x", OutputLimit))
			for range 8 {
				if n, err := output.Write(p); n != len(p) || err != nil {
					t.Errorf("write = %d, %v", n, err)
				}
				if len(output.String()) > OutputLimit {
					t.Error("output exceeds limit")
				}
			}
		})
	}
	wg.Wait()
	if len(output.String()) != OutputLimit {
		t.Fatalf("output size = %d", len(output.String()))
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
