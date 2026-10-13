//go:build linux

package host

import (
	"bytes"
	"errors"
	"sync"

	"github.com/ShaulLavo/brine/internal/localexec"
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
	switch localexec.InspectExitReason(err) {
	case localexec.ExitUnknown:
		return "guest_child_stop"
	case localexec.ExitInterrupt:
		return "guest_child_stop_sigint"
	case localexec.ExitKilled:
		return "guest_child_stop_sigkill"
	case localexec.ExitSignal:
		return "guest_child_stop_signal"
	}
	if panicMarker {
		return "guest_child_stop_panic"
	}
	return "guest_child_stop_exit"
}
