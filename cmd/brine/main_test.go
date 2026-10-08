package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestRunExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		err  error
		exit int
	}{
		{"success", []string{"version", "--json"}, nil, 0},
		{"operational", []string{"tui"}, result.New(result.InternalError, nil), 1},
		{"usage", []string{"unknown", "--json"}, nil, 2},
		{"dependency", []string{"tui"}, result.New(result.DependencyMissing, nil), 3},
		{"policy", []string{"tui"}, result.New(result.PolicyRefused, nil), 4},
		{"conflict", []string{"tui"}, result.New(result.Conflict, nil), 5},
		{"recovery", []string{"tui"}, result.New(result.RecoveryRequired, nil), 6},
		{"interruption", []string{"tui"}, context.Canceled, 130},
		{"unclassified", []string{"tui"}, errors.New("unsafe output"), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := cli.Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &diagnostics, Version: "fixture", RunTUI: func(context.Context, io.Reader, io.Writer) error { return tt.err }}
			if tt.args[0] == "tui" {
				terminal, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
				if err != nil {
					t.Skip("PTY unavailable", err)
				}
				t.Cleanup(func() { terminal.Close() })
				deps.Stdin = terminal
				deps.Stdout = terminal
			}
			if got := run(deps, tt.args); got != tt.exit {
				t.Fatalf("exit = %d; want %d", got, tt.exit)
			}
			if strings.Contains(diagnostics.String(), "unsafe output") {
				t.Fatal("unsafe diagnostic")
			}
		})
	}
}
