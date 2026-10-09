package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
)

type forbiddenRead struct{}

func (forbiddenRead) Read([]byte) (int, error) { panic("root must refuse without reading stdin") }

func TestHostRootRefusal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := Dependencies{Context: context.Background(), Stdin: forbiddenRead{}, Stdout: &stdout, Stderr: &stderr, Version: "test", HostUID: func() int { return 0 }}
	err := Execute(deps, []string{"host", "serve", "--json", "--jsonl", "--help"})
	if result.ExitCode(err) != 4 {
		t.Fatalf("%v", err)
	}
	var envelope result.Envelope
	d := json.NewDecoder(&stdout)
	if err := d.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != result.DispatchRootRefused {
		t.Fatalf("%+v", envelope)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		t.Fatal("extra JSON")
	}
}

func TestHostServeHiddenAndWriteFailure(t *testing.T) {
	deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(`{"schema_version":1,"op":"ping","request_id":"test","args":{}}`), Stdout: io.Discard, Stderr: io.Discard, Version: "test", HostUID: func() int { return 1 }}
	root := NewRootCommand(deps)
	host, _, err := root.Find([]string{"host"})
	if err != nil || !host.Hidden {
		t.Fatal("host visible")
	}
	deps.Stdout = failingWriter{err: io.ErrClosedPipe}
	if result.ExitCode(Execute(deps, []string{"host", "serve"})) != 1 {
		t.Fatal("write failure not operational")
	}
}

func TestHostServeCanceledInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stdin, writer := io.Pipe()
	defer writer.Close()
	defer stdin.Close()
	var stdout bytes.Buffer
	deps := Dependencies{Context: ctx, Stdin: stdin, Stdout: &stdout, Stderr: io.Discard, Version: "test", HostUID: func() int { return 1 }}
	done := make(chan error, 1)
	go func() { done <- Execute(deps, []string{"host", "serve"}) }()
	// A synchronous pipe write proves the request read has begun and is waiting for EOF.
	if _, err := writer.Write([]byte(`{"schema_version":1,`)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if result.ExitCode(err) != 130 {
			t.Fatalf("%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled input did not stop")
	}
}

func TestHostCommandResolution(t *testing.T) {
	for _, args := range [][]string{{"host", "serve"}, {"--json", "host", "serve"}, {"--no-input=false", "host", "serve"}, {"host", "--json", "serve"}, {"--json", "host", "serve", "--jsonl", "--help"}} {
		if !HostServeRequested(args) {
			t.Fatalf("missed %q", args)
		}
	}
	for _, args := range [][]string{nil, {"version", "host", "serve"}, {"help", "host", "serve"}, {"--json", "host"}, {"host", "unknown"}} {
		if HostServeRequested(args) {
			t.Fatalf("misidentified %q", args)
		}
	}
}
