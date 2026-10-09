package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
)

func TestFileInputSizeAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", dispatch.RequestLimit*2)), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	input := &boundedFileInput{file: file, closed: make(chan struct{})}
	defer input.Close()
	data, err := io.ReadAll(input)
	if err != nil || len(data) != dispatch.RequestLimit+1 {
		t.Fatalf("size %d err %v", len(data), err)
	}
	blocked, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	input = &boundedFileInput{file: blocked, closed: make(chan struct{})}
	defer input.Close()
	done := make(chan error, 1)
	go func() { _, err := io.ReadAll(input); done <- err }()
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("file input cancellation blocked")
	}
}

func TestFileInputTimer(t *testing.T) {
	source, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	input := &boundedFileInput{file: source, closed: make(chan struct{})}
	defer input.Close()
	start := time.Now()
	_, err = io.ReadAll(input)
	if !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) > 7*time.Second {
		t.Fatalf("timer err %v after %v", err, time.Since(start))
	}
}
