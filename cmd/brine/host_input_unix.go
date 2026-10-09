//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"syscall"
	"time"
)

func hostInput() io.ReadCloser {
	fd, err := syscall.Dup(int(os.Stdin.Fd()))
	if err != nil {
		return failedInput{err}
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return failedInput{err}
	}
	// Inherited blocking descriptors are not registered with Go's poller. A
	// nonblocking duplicate gives this wrapper interruptible reads and deadlines.
	file := os.NewFile(uintptr(fd), "dispatcher-stdin")
	if err := file.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		if errors.Is(err, os.ErrNoDeadline) {
			info, statErr := file.Stat()
			if statErr == nil && info.Mode().IsRegular() {
				return &boundedFileInput{file: file, closed: make(chan struct{})}
			}
		}
		_ = file.Close()
		return failedInput{err}
	}
	return file
}
