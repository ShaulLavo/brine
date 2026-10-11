package localexec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStreamHelper(t *testing.T) {
	if os.Getenv("BRINE_STREAM_HELPER") != "1" {
		return
	}
	if os.Getenv("BRINE_STREAM_AMBIENT") != "" {
		os.Exit(9)
	}
	switch os.Getenv("BRINE_STREAM_MODE") {
	case "echo":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(8)
		}
	case "diagnostics":
		if _, err := io.WriteString(os.Stderr, strings.Repeat("private-diagnostic", 2000)); err != nil {
			os.Exit(8)
		}
	case "timeout":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func TestStreamEnvironmentQuotasAndUnknownMutation(t *testing.T) {
	t.Setenv("BRINE_STREAM_AMBIENT", "must-not-inherit")
	for _, mode := range []string{"echo", "diagnostics", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			budget := 3 * time.Second
			if mode == "timeout" {
				budget = 200 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			var output bytes.Buffer
			command := Command{Path: os.Args[0], Args: []string{"-test.run=^TestStreamHelper$"}, Dir: t.TempDir(), Env: []string{"PATH=/usr/bin:/bin", "BRINE_STREAM_HELPER=1", "BRINE_STREAM_MODE=" + mode}, OutputLimit: 256, Mutation: true}
			err := (ExecRunner{}).RunStreams(ctx, command, strings.NewReader("bounded input"), &output)
			if mode == "echo" {
				if err != nil || output.String() != "bounded input" {
					t.Fatal("explicit stream environment", err)
				}
				return
			}
			var operation *Error
			if !errors.As(err, &operation) || operation.Kind != UnknownOutcome {
				t.Fatal("interrupted mutation appeared settled", err)
			}
			if strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatal("diagnostics escaped")
			}
		})
	}
}
