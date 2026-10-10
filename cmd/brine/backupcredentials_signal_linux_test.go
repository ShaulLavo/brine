package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
	"golang.org/x/sys/unix"
)

func TestBinaryBackupCredentialSignalWithOpenWriter(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "brine")
	buildCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, signal := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			if _, err := writer.Write([]byte("PLANTED_PRIVATE_PIPE_SECRET")); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			cmd := exec.Command(binary, "backup", "credentials", "set", "hello", "--plan-id", "sha256:"+strings.Repeat("a", 64), "--target", "fixture", "--config-dir", t.TempDir(), "--jsonl")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = reader, &out, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			finished := false
			defer func() {
				if !finished {
					cmd.Process.Kill()
					<-done
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				pending, err := unix.IoctlGetInt(int(reader.Fd()), unix.TIOCINQ)
				if err != nil {
					t.Fatal(err)
				}
				if pending == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not consume partial stdin")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				finished = true
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 130 {
					t.Fatalf("err=%v stderr=%s", err, stderr.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("signal did not interrupt credential stdin with writer still open")
			}
			decoder := json.NewDecoder(&out)
			var envelope result.Envelope
			if decoder.Decode(&envelope) != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != result.Interrupted {
				t.Fatalf("envelope=%+v", envelope)
			}
			if decoder.Decode(new(any)) != io.EOF {
				t.Fatal("more than one error envelope")
			}
			if strings.Contains(out.String()+stderr.String(), "PLANTED_PRIVATE_PIPE_SECRET") {
				t.Fatal("secret leaked")
			}
		})
	}
}
