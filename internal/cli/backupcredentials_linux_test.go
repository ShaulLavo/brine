package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
	"golang.org/x/sys/unix"
)

func TestBackupCredentialOpenPipeCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if _, err := writer.Write([]byte(`{"access_key_id":"PLANTED_PRIVATE"`)); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps.Context, deps.Stdin = ctx, reader
	done := make(chan error, 1)
	go func() {
		done <- Execute(deps, []string{"backup", "credentials", "set", "hello", "--plan-id", "sha256:" + strings.Repeat("a", 64), "--target", "fixture", "--json"})
	}()
	finished := false
	defer func() {
		if !finished {
			writer.Close()
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
			t.Fatal("command did not consume partial stdin")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		finished = true
		if result.ExitCode(err) != 130 {
			t.Fatal("cancellation lost", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation left a credential read blocked with writer open")
	}
	if strings.Contains(out.String()+stderr.String(), "PLANTED") {
		t.Fatal("partial credential leaked")
	}
	// The command borrows stdin: cancellation must not close the caller's file.
	if _, err := reader.Stat(); err != nil {
		t.Fatal("borrowed reader closed", err)
	}
}
