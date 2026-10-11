package transport

import (
	"context"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
)

const diagnosticTailLimit = 2048

func transportFailure(operation string, stderr []byte, cause error) error {
	reason := "runner_failure"
	var exit interface{ ExitCode() int }
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		reason = "deadline_exceeded"
	case errors.Is(cause, context.Canceled):
		reason = "canceled"
	case errors.Is(cause, localexec.ErrOutputLimit):
		reason = "output_limit"
	case errors.As(cause, &exit) && exit.ExitCode() == 255:
		reason = "ssh_exit_255"
	}
	return result.TransportError(cause, result.TransportDiagnostics{Operation: operation, Reason: reason, SSHStderrTail: safeStderrTail(stderr)})
}

func safeStderrTail(stderr []byte) string {
	if len(stderr) > diagnosticTailLimit {
		stderr = stderr[len(stderr)-diagnosticTailLimit:]
	}
	lines := strings.Split(string(stderr), "\n")
	if len(lines) > 16 {
		lines = lines[len(lines)-16:]
	}
	safe := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		diagnostic := "[redacted]"
		// Arbitrary remote stderr can contain credentials or host keys. Keep only
		// fixed SSH failure phrases, never addresses, paths, fingerprints or banners.
		for _, phrase := range []string{
			"Connection refused", "Connection timed out", "Connection reset by peer",
			"No route to host", "Network is unreachable", "Host key verification failed.",
			"Permission denied (publickey).", "Too many authentication failures",
			"Could not resolve hostname", "REMOTE HOST IDENTIFICATION HAS CHANGED!",
			"Broken pipe", "Connection closed", "Operation timed out",
		} {
			if strings.Contains(line, phrase) {
				diagnostic = phrase
				break
			}
		}
		safe = append(safe, diagnostic)
	}
	return strings.Join(safe, "\n")
}
