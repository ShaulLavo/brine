package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEngineStartupCompositionNeverOpensDeployRuntime(t *testing.T) {
	deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	opened, called, closed := 0, 0, 0
	lifecycle := RuntimeLifecycle{
		Open: func(context.Context, bool) (RuntimeServices, error) {
			t.Fatal("engine opened deployment runtime")
			return RuntimeServices{}, nil
		},
		OpenEngineStartup: func(context.Context) (RuntimeServices, error) {
			opened++
			return RuntimeServices{EngineExec: func(context.Context, string) error { called++; return nil }, Close: func() error { closed++; return nil }}, nil
		},
	}
	if err := ExecuteWithRuntime(deps, []string{"host", "engine-exec", strings.Repeat("a", 32)}, lifecycle); err != nil || opened != 1 || called != 1 || closed != 1 {
		t.Fatal("startup did not use its narrow lazy runtime", err, opened, called, closed)
	}
}

func TestEngineForegroundCancellationRequiresSuccessfulAbsence(t *testing.T) {
	for _, outcome := range []error{nil, errors.New("runtime absence unknown")} {
		ctx, cancel := context.WithCancel(context.Background())
		deps := Dependencies{Context: ctx, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		deps.HostEngineExec = func(context.Context, string) error { cancel(); return outcome }
		err := Execute(deps, []string{"host", "engine-exec", strings.Repeat("a", 32)})
		if outcome == nil && err != nil || outcome != nil && err == nil {
			t.Fatal("foreground completion lost proven absence outcome", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	deps := Dependencies{Context: ctx, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	deps.HostWriterAttempt = func(context.Context, string) error { cancel(); return nil }
	if Execute(deps, []string{"host", "writer-attempt", strings.Repeat("a", 32)}) == nil {
		t.Fatal("ordinary command ignored cancellation")
	}
}

func TestEngineCommandsValidateBeforeEffects(t *testing.T) {
	for _, args := range [][]string{{"host", "engine-exec"}, {"host", "engine-exec", "../data"}, {"host", "engine-exec", strings.Repeat("a", 32), "extra"}, {"host", "engine-exec", "--help"}, {"host", "engine-stop"}, {"host", "engine-stop", "../data"}, {"host", "engine-stop", strings.Repeat("a", 32), "extra"}, {"host", "engine-stop", "--help"}, {"host", "engine-copy-observe", "extra"}, {"host", "engine-copy-observe", "--help"}} {
		deps := Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
		deps.HostEngineCopyObserve = func(context.Context, io.Reader, io.Writer) error {
			t.Fatal("invalid observe command had effects")
			return nil
		}
		_ = ExecuteWithRuntime(deps, args, RuntimeLifecycle{OpenEngineStartup: func(context.Context) (RuntimeServices, error) {
			t.Fatal("invalid engine command opened state")
			return RuntimeServices{}, nil
		}})
	}
}

func TestEngineStopCompositionUsesOnlyNarrowRuntime(t *testing.T) {
	deps := Dependencies{Context: t.Context(), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	called, closed := 0, 0
	lifecycle := RuntimeLifecycle{Open: func(context.Context, bool) (RuntimeServices, error) {
		t.Fatal("cleanup opened deployment runtime")
		return RuntimeServices{}, nil
	}, OpenEngineStartup: func(context.Context) (RuntimeServices, error) {
		return RuntimeServices{EngineStop: func(context.Context, string) error { called++; return nil }, Close: func() error { closed++; return nil }}, nil
	}}
	if err := ExecuteWithRuntime(deps, []string{"host", "engine-stop", strings.Repeat("a", 32)}, lifecycle); err != nil || called != 1 || closed != 1 {
		t.Fatal("cleanup did not own narrow composition", err, called, closed)
	}
}
