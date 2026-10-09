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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestHostServeBinaryBoundary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "brine")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build %v %s", err, out)
	}
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	payload := "touch " + marker + "; echo synthetic-private-payload"
	for _, tt := range []struct {
		name, input string
		exit        int
	}{
		{"ping", `{"schema_version":1,"op":"ping","request_id":"fixture","args":{}}`, 0},
		{"unknown", `{"schema_version":1,"op":"shell","request_id":"fixture","args":{}}`, 4},
		{"two requests", `{"schema_version":1,"op":"ping","request_id":"fixture","args":{}} {}`, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, binary, "host", "serve", "--json", "--jsonl", "--bad-flag", payload)
			cmd.Env = []string{"SSH_ORIGINAL_COMMAND=" + payload, "HOME=/does-not-exist", "PATH=/does-not-exist", "BASH_ENV=" + marker, "BRINE_TARGET=" + payload}
			cmd.Stdin = strings.NewReader(tt.input)
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			exit := 0
			if err != nil {
				var status *exec.ExitError
				if !errors.As(err, &status) {
					t.Fatal(err)
				}
				exit = status.ExitCode()
			}
			want := tt.exit
			if os.Geteuid() == 0 {
				want = 4
			}
			if exit != want {
				t.Fatalf("exit %d want %d, stdout=%s stderr=%s", exit, want, stdout.String(), stderr.String())
			}
			d := json.NewDecoder(&stdout)
			var envelope result.Envelope
			if err := d.Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if err := d.Decode(new(any)); err != io.EOF {
				t.Fatal("extra response")
			}
			if envelope.OK != (want == 0) {
				t.Fatalf("%+v", envelope)
			}
			if os.Geteuid() == 0 && envelope.Error.Code != result.DispatchRootRefused {
				t.Fatal("missing root refusal")
			}
			if strings.Contains(stderr.String(), "synthetic-private") || strings.Contains(stdout.String(), "synthetic-private") {
				t.Fatal("payload leaked")
			}
			if !strings.Contains(stderr.String(), "ssh_original_command_length="+strconv.Itoa(len(payload))) {
				t.Fatalf("length audit missing %q", stderr.String())
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("payload executed %v", err)
			}
		})
	}
	t.Run("incomplete stdin deadline", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root refuses before stdin")
		}
		cmd := exec.CommandContext(ctx, binary, "host", "serve")
		cmd.Env = []string{}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		defer stdin.Close()
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, writeErr := io.WriteString(stdin, `{"schema_version":1,"op":"ping","request_id":"fixture","args":{}}`)
		waitErr := cmd.Wait()
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("%v %s", waitErr, stdout.String())
		}
		if time.Since(start) > 8*time.Second {
			t.Fatal("stdin deadline not bounded")
		}
		var envelope result.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != result.DispatchInvalidRequest {
			t.Fatalf("%+v", envelope)
		}
	})

	t.Run("interrupt during stdin", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root refuses before stdin")
		}
		cmd := exec.CommandContext(ctx, binary, "host", "serve")
		cmd.Env = []string{}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		defer stdin.Close()
		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		line, readErr := bufio.NewReader(stderr).ReadString('\n')
		if readErr == nil {
			_, readErr = io.WriteString(stdin, `{"schema_version":1,`)
		}
		start := time.Now()
		signalErr := cmd.Process.Signal(os.Interrupt)
		waitErr := cmd.Wait()
		if readErr != nil || signalErr != nil || !strings.Contains(line, "ssh_original_command_length=0") {
			t.Fatalf("%v %v %q", readErr, signalErr, line)
		}
		var exit *exec.ExitError
		if !errors.As(waitErr, &exit) || exit.ExitCode() != 130 {
			t.Fatalf("%v %s", waitErr, stdout.String())
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("interrupt did not unblock input")
		}
		var envelope result.Envelope
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error == nil || envelope.Error.Code != result.Interrupted {
			t.Fatalf("%+v", envelope)
		}
	})

}
