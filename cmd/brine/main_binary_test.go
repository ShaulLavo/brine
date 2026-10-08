package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestBinaryMachineResponses(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "brine")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tt := range []struct {
		args    []string
		exit    int
		command string
	}{
		{[]string{"version", "--json", "--no-input"}, 0, "brine version"},
		{[]string{"doctor", "--json", "--no-input"}, 0, "brine doctor"},
		{[]string{"synthetic-secret", "--json"}, 2, "brine"},
		{[]string{"version", "--synthetic-secret", "--json"}, 2, "brine version"},
		{[]string{"tui", "--json"}, 2, "brine tui"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			cmd := exec.CommandContext(ctx, binary, tt.args...)
			cmd.Stdout = &out
			cmd.Stderr = &diagnostics
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tt.exit {
				t.Fatalf("exit = %d, want %d; stderr = %q", code, tt.exit, diagnostics.String())
			}
			dec := json.NewDecoder(&out)
			var got result.Envelope
			if err := dec.Decode(&got); err != nil {
				t.Fatal(err)
			}
			if err := dec.Decode(new(any)); err != io.EOF {
				t.Fatalf("extra output = %v", err)
			}
			if got.SchemaVersion != 1 || got.Command != tt.command || got.OK != (tt.exit == 0) {
				t.Fatalf("envelope = %+v", got)
			}
			if strings.Contains(diagnostics.String(), "synthetic-secret") || strings.Contains(diagnostics.String(), "\x1b") {
				t.Fatal("unsafe diagnostics")
			}
		})
	}
}

func TestBinaryCompletionProtocol(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "brine")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	for _, command := range []string{"__complete", "__completeNoDesc"} {
		for _, withJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json-target=%t", command, withJSON), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				args := []string{command, "version", "--j"}
				wantFlag := "--json"
				if withJSON {
					args = []string{command, "version", "--json", "--no-input", "--h"}
					wantFlag = "--help"
				}
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Stdout = &out
				cmd.Stderr = &diagnostics
				if err := cmd.Run(); err != nil {
					t.Fatalf("completion: %v, stderr = %q", err, diagnostics.String())
				}
				if !strings.Contains(out.String(), wantFlag) || !strings.HasSuffix(out.String(), ":4\n") {
					t.Fatalf("protocol stdout = %q", out.String())
				}
				if command == "__completeNoDesc" && strings.Contains(out.String(), "\t") {
					t.Fatalf("description in %q", out.String())
				}
			})
		}
	}
}
