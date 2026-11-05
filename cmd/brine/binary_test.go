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

func runCommandTests(m *testing.M) (code int, interrupted os.Signal) {
	dir, err := os.MkdirTemp("", "brine-command-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "binary temp directory: %v\n", err)
		return 1, nil
	}
	defer func() { code = cleanCommandTestBinary(dir, code) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	interrupts := make(chan os.Signal, 1)
	for _, value := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		if !signal.Ignored(value) {
			signal.Notify(interrupts, value)
		}
	}
	defer signal.Stop(interrupts)

	binary := filepath.Join(dir, "brine")
	commandTestBinary = binary
	built := make(chan struct{})
	result := make(chan int, 1)
	go func() {
		build := exec.CommandContext(ctx, "go", "build", "-o")
		build.Args = append(build.Args, binary, ".")
		configureCommandTestBuild(build)
		build.WaitDelay = time.Second
		out, err := build.CombinedOutput()
		stopCommandTestBuild(build)
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

func cleanCommandTestBinary(dir string, code int) int {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "binary cleanup: %v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}
