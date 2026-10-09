package localexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestInputHelper(t *testing.T) {
	if !slices.Contains(os.Args, "BRINE_INPUT_HELPER=1") {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "echo":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(7)
		}
		fmt.Fprint(os.Stdout, string(data))
		fmt.Fprint(os.Stderr, os.Getenv("BRINE_INPUT_PRIVATE"))
	case "stdout overflow":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 10000))
	case "stderr overflow":
		fmt.Fprint(os.Stdout, "{}")
		fmt.Fprint(os.Stderr, strings.Repeat("x", 10000))
	case "timeout":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func TestInputExecutionBoundsAndEnvironment(t *testing.T) {
	t.Setenv("BRINE_INPUT_PRIVATE", "must-not-inherit")
	for _, mode := range []string{"echo", "stdout overflow", "stderr overflow", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			timeout := 3 * time.Second
			if mode == "timeout" {
				timeout = 250 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			output, err := (ExecRunner{}).RunInput(ctx, Command{Path: os.Args[0], Args: []string{"-test.run=^TestInputHelper$", "--", "BRINE_INPUT_HELPER=1", mode}, Stdin: []byte("request input"), OutputLimit: 256})
			switch mode {
			case "echo":
				if err != nil || string(output.Stdout) != "request input" || string(output.Stderr) != helperCoverageWarning() {
					t.Fatalf("%+v %v", output, err)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%v", err)
				}
			default:
				if !errors.Is(err, ErrOutputLimit) || len(output.Stdout) > 256 || len(output.Stderr) > 256 {
					t.Fatalf("%+v %v", output, err)
				}
			}
		})
	}
}

func helperCoverageWarning() string {
	if testing.CoverMode() != "" {
		return "warning: GOCOVERDIR not set, no coverage data emitted\n"
	}
	return ""
}
