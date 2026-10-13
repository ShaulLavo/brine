//go:build linux

package host

import (
	"bytes"
	"errors"
	"os/exec"
	"sync"
	"syscall"
)

type moduleNativeDiagnostics struct {
	mu          sync.Mutex
	tail        []byte
	panicMarker bool
}

func (d *moduleNativeDiagnostics) Write(input []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	size := len(input)
	// The overlap detects a fixed marker split across writes; no text is exposed.
	for len(input) != 0 {
		var window [128]byte
		overlap := copy(window[:], d.tail)
		consumed := copy(window[overlap:], input)
		current := window[:overlap+consumed]
		d.panicMarker = d.panicMarker || bytes.Contains(current, []byte("panicked at")) || bytes.Contains(current, []byte("fatal runtime error:"))
		keep := min(len(current), 64)
		d.tail = append(d.tail[:0], current[len(current)-keep:]...)
		clear(window[:])
		input = input[consumed:]
	}
	return size, nil
}

func (d *moduleNativeDiagnostics) panicSeen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.panicMarker
}

func moduleChildStopFailureCode(err error, panicMarker bool) string {
	var stopped engineChildStopError
	if !errors.As(err, &stopped) {
		return "guest_child_stop"
	}
	switch stopped.phase {
	case "interrupt":
		return "guest_child_stop_interrupt"
	case "context":
		return "guest_child_stop_context"
	case "budget":
		return "guest_child_stop_budget"
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		return "guest_child_stop"
	}
	status, ok := exited.Sys().(syscall.WaitStatus)
	if !ok {
		return "guest_child_stop"
	}
	if status.Signaled() {
		switch status.Signal() {
		case syscall.SIGINT:
			return "guest_child_stop_sigint"
		case syscall.SIGKILL:
			return "guest_child_stop_sigkill"
		default:
			return "guest_child_stop_signal"
		}
	}
	if panicMarker {
		return "guest_child_stop_panic"
	}
	return "guest_child_stop_exit"
}
