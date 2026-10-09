package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestHostReviewRegressions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root refuses before stdin")
	}
	binary := filepath.Join(t.TempDir(), "brine")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	request := `{"schema_version":1,"op":"ping","request_id":"fixture","args":{}}`
	t.Run("regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(path, []byte(request), 0600); err != nil {
			t.Fatal(err)
		}
		input, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		cmd := exec.CommandContext(ctx, binary, "host", "serve")
		cmd.Stdin = input
		var output bytes.Buffer
		cmd.Stdout = &output
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v %s", err, output.String())
		}
		var envelope result.Envelope
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || !envelope.OK {
			t.Fatalf("%v %+v", err, envelope)
		}
	})
	t.Run("leading and trailing flags", func(t *testing.T) {
		cmd := exec.CommandContext(ctx, binary, "--json", "host", "serve", "--jsonl", "--help", "synthetic-payload")
		cmd.Env = []string{"SSH_ORIGINAL_COMMAND=synthetic-payload", "HOME=/invalid", "PATH=/invalid"}
		cmd.Stdin = strings.NewReader(request)
		var output, audit bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &audit
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v %s", err, output.String())
		}
		var envelope result.Envelope
		if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || !envelope.OK {
			t.Fatalf("%v %+v", err, envelope)
		}
		if !strings.Contains(audit.String(), "ssh_original_command_length=17") {
			t.Fatalf("%s", audit.String())
		}
	})
	for _, flag := range []string{"--json", "--no-input"} {
		for _, interrupt := range []bool{false, true} {
			name := flag + "/deadline"
			if interrupt {
				name = flag + "/interrupt"
			}
			t.Run(name, func(t *testing.T) {
				bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				cmd := exec.CommandContext(bounded, binary, flag, "host", "serve")
				cmd.Env = []string{"SSH_ORIGINAL_COMMAND=synthetic-payload", "HOME=/invalid", "PATH=/invalid"}
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				stderr, err := cmd.StderrPipe()
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				cmd.Stdout = &output
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				line, readErr := bufio.NewReader(stderr).ReadString('\n')
				_, writeErr := io.WriteString(stdin, request)
				start := time.Now()
				if interrupt {
					if err := cmd.Process.Signal(os.Interrupt); err != nil {
						t.Fatal(err)
					}
				}
				waitErr := cmd.Wait()
				if readErr != nil || writeErr != nil || !strings.Contains(line, "ssh_original_command_length=17") {
					t.Fatalf("audit %q read=%v write=%v result=%v", line, readErr, writeErr, waitErr)
				}
				var exit *exec.ExitError
				want := 2
				if interrupt {
					want = 130
				}
				if !errors.As(waitErr, &exit) || exit.ExitCode() != want {
					t.Fatalf("%v %s", waitErr, output.String())
				}
				if interrupt && time.Since(start) > 2*time.Second {
					t.Fatal("interrupt failed to unblock")
				}
				var envelope result.Envelope
				if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				code := result.DispatchInvalidRequest
				if interrupt {
					code = result.Interrupted
				}
				if envelope.Error == nil || envelope.Error.Code != code {
					t.Fatalf("%+v", envelope)
				}
			})
		}
	}
}
