package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/secrets"
	"golang.org/x/sys/unix"
)

type borrowedBlockingInput struct{ read bool }

func (r *borrowedBlockingInput) Read([]byte) (int, error) {
	r.read = true
	return 0, errors.New("must not read")
}

type contextSecretInput struct{ buffer []byte }

func (r *contextSecretInput) Read([]byte) (int, error) { panic("use ReadContext") }
func (r *contextSecretInput) ReadContext(ctx context.Context, p []byte) (int, error) {
	r.buffer = p
	copy(p, "private")
	return 7, errors.New("private read failure")
}

func TestSecretInputRejectsBorrowedBlockingReader(t *testing.T) {
	reader := &borrowedBlockingInput{}
	_, err := readSecretInput(context.Background(), reader)
	if result.ExitCode(err) != 2 || reader.read {
		t.Fatalf("err=%v read=%t", err, reader.read)
	}
}

func TestSecretInputClearsPartialReadOnError(t *testing.T) {
	reader := &contextSecretInput{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := readSecretInput(ctx, reader)
	if err == nil || !bytes.Equal(reader.buffer, make([]byte, secrets.ValueLimit+1)) {
		t.Fatal("failed read retained secret data")
	}
}

func TestSecretInputRegularFileAndExactLimit(t *testing.T) {
	for _, size := range []int{1, secrets.ValueLimit, secrets.ValueLimit + 1} {
		file, err := os.CreateTemp(t.TempDir(), "secret")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		value := strings.Repeat("x", size)
		if _, err := file.WriteString(value); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		input, err := readSecretInput(context.Background(), file)
		if err != nil || string(input) != value {
			t.Fatalf("size=%d err=%v got=%d", size, err, len(input))
		}
		clear(input)
	}
}

func TestSecretInputClosedFileIsInvalidUsage(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = readSecretInput(ctx, file)
	if result.Classify(err).Code() != result.InvalidUsage {
		t.Fatalf("closed file should fail before polling: %v", err)
	}
}

func TestSecretInputPreservesFileFlags(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	flags := func() int {
		raw, err := reader.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var value int
		var controlErr error
		if err := raw.Control(func(fd uintptr) { value, controlErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
			t.Fatal(err)
		}
		if controlErr != nil {
			t.Fatal(controlErr)
		}
		return value
	}
	before := flags()
	if _, err := writer.Write([]byte("private")); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	input, err := readSecretInput(context.Background(), reader)
	defer clear(input)
	if err != nil || string(input) != "private" {
		t.Fatal("pipe read failed", err)
	}
	if after := flags(); before != after {
		t.Fatalf("borrowed descriptor flags changed: %d -> %d", before, after)
	}
}

func TestSecretInvalidConfigurationDoesNotConsumeStdin(t *testing.T) {
	var out, stderr bytes.Buffer
	deps := testDependencies(t, &out, &stderr)
	input := bytes.NewBufferString("private-input")
	deps.Stdin = input
	err := Execute(deps, []string{"secret", "set", "hello", "TOKEN", "--target", "fixture", "--config-dir", "/fixture/\x1bprivate"})
	if result.ExitCode(err) != 2 || input.String() != "private-input" {
		t.Fatalf("invalid configuration consumed secret stdin: exit=%d remaining=%d", result.ExitCode(err), input.Len())
	}
}
