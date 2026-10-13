//go:build linux

package localexec

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

func TestInspectExitReasonPreservesWrappedNativeFailure(t *testing.T) {
	for _, scenario := range []struct {
		script string
		reason ExitReason
	}{
		{script: "exit 9", reason: ExitNonzero},
		{script: "kill -2 $$", reason: ExitInterrupt},
		{script: "kill -9 $$", reason: ExitKilled},
		{script: "kill -15 $$", reason: ExitSignal},
	} {
		command := exec.CommandContext(t.Context(), "/bin/sh", "-c", scenario.script) //nolint:gosec // Closed test-only scripts above, without caller input.
		original := command.Run()
		var exited *exec.ExitError
		if !errors.As(original, &exited) {
			t.Fatal("native exit fixture did not fail")
		}
		wrapped := fmt.Errorf("closed test wrapper: %w", original)
		if got := InspectExitReason(wrapped); got != scenario.reason || !errors.Is(wrapped, original) {
			t.Fatal("closed classification changed reason or original cause", got)
		}
	}
}

func TestInspectExitReasonRefusesMissingNativeStatus(t *testing.T) {
	for _, err := range []error{nil, errors.New("fixed test failure"), &exec.ExitError{}} {
		if InspectExitReason(err) != ExitUnknown {
			t.Fatal("missing native status acquired a reason")
		}
	}
}
