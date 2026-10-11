package cli

import (
	"bytes"
	"context"
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

func TestEngineCommandsValidateBeforeEffects(t *testing.T) {
	for _, args := range [][]string{{"host", "engine-exec"}, {"host", "engine-exec", "../data"}, {"host", "engine-exec", strings.Repeat("a", 32), "extra"}, {"host", "engine-exec", "--help"}, {"host", "engine-copy-observe", "extra"}, {"host", "engine-copy-observe", "--help"}} {
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
