package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

var commandTestBinary string

func TestMain(m *testing.M) {
	code, interrupted := runCommandTests(m)
	if interrupted == nil {
		os.Exit(code)
	}
	signal.Reset(interrupted)
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		fmt.Fprintf(os.Stderr, "signal: %v\n", err)
		os.Exit(code)
	}
	if err := process.Signal(interrupted); err != nil {
		fmt.Fprintf(os.Stderr, "signal: %v\n", err)
		os.Exit(code)
	}
	select {}
}

func runCommandTests(m *testing.M) (int, os.Signal) {
	dir, err := os.MkdirTemp("", "brine-command-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "binary temp directory: %v\n", err)
		return 1, nil
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "binary cleanup: %v\n", err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)

	commandTestBinary = filepath.Join(dir, "brine")
	built := make(chan struct{})
	result := make(chan int, 1)
	go func() {
		out, err := exec.CommandContext(ctx, "go", "build", "-o", commandTestBinary, ".").CombinedOutput()
		close(built)
		if err != nil {
			fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
			result <- 1
			return
		}
		result <- m.Run()
	}()
	select {
	case code := <-result:
		return code, nil
	case interrupted := <-interrupts:
		cancel()
		<-built
		return 128 + int(interrupted.(syscall.Signal)), interrupted
	}
}
