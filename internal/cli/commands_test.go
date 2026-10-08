package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func testDependencies(t *testing.T, stdout, stderr io.Writer) Dependencies {
	t.Helper()
	return Dependencies{
		Context: context.Background(),
		Stdin:   strings.NewReader(""),
		Stdout:  stdout,
		Stderr:  stderr,
		Version: "0.1.0-dev",
		LookPath: func(name string) (string, error) {
			t.Fatalf("unexpected lookup for %q", name)
			return "", errors.New("unexpected lookup")
		},
		RunTUI: func(context.Context, io.Reader, io.Writer) error {
			t.Fatal("unexpected TUI invocation")
			return nil
		},
	}
}

func assertStreams(t *testing.T, stdout, stderr *bytes.Buffer, want string) {
	t.Helper()
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q; want stdout = %q and empty stderr", stdout.String(), stderr.String(), want)
	}
}

func TestVersionOutput(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"human", []string{"version"}, "brine 0.1.0-dev\n"},
		{"json", []string{"version", "--json", "--no-input"}, "{\"schema_version\":1,\"command\":\"brine version\",\"ok\":true,\"data\":{\"version\":\"0.1.0-dev\"},\"error\":null}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := NewRootCommand(testDependencies(t, &stdout, &stderr))
			root.SetArgs(tt.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			assertStreams(t, &stdout, &stderr, tt.want)
		})
	}
}

func TestVersionSource(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := testDependencies(t, &stdout, &stderr)
	deps.Version = "test-version"
	root := NewRootCommand(deps)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	assertStreams(t, &stdout, &stderr, "brine test-version\n")
}

func TestTUIFlagRefusal(t *testing.T) {
	for _, args := range [][]string{
		{"tui", "--json"},
		{"--no-input", "tui"},
		{"tui", "--json", "--no-input"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := NewRootCommand(testDependencies(t, &stdout, &stderr))
			root.SetArgs(args)
			err := root.Execute()
			if err == nil || err.Error() != "tui is interactive; remove --json and --no-input" {
				t.Fatalf("error = %v", err)
			}
			assertStreams(t, &stdout, &stderr, "")
		})
	}
}

func TestTUIReceivesContextAndStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := testDependencies(t, &stdout, &stderr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps.Context = ctx
	deps.Stdin = strings.NewReader("test input")
	wantErr := errors.New("runner failure")
	called := false
	deps.RunTUI = func(gotCtx context.Context, stdin io.Reader, output io.Writer) error {
		called = true
		if gotCtx != ctx || stdin != deps.Stdin || output != &stdout {
			t.Fatal("runner did not receive injected context and streams")
		}
		cancel()
		if !errors.Is(gotCtx.Err(), context.Canceled) {
			t.Fatal("runner context did not receive cancellation")
		}
		return wantErr
	}
	root := NewRootCommand(deps)
	root.SetArgs([]string{"tui"})
	if err := root.Execute(); err != wantErr {
		t.Fatalf("error = %v; want unchanged runner error", err)
	}
	if !called {
		t.Fatal("runner was not called")
	}
	assertStreams(t, &stdout, &stderr, "")
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestJSONWriterError(t *testing.T) {
	for _, command := range []string{"version", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var stderr bytes.Buffer
			wantErr := errors.New("write failure")
			deps := testDependencies(t, failingWriter{wantErr}, &stderr)
			configureDoctorFixture(t, &deps)
			root := NewRootCommand(deps)
			root.SetArgs([]string{command, "--json"})
			if err := root.Execute(); err != wantErr {
				t.Fatalf("error = %v; want unchanged write error", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q; want empty stderr", stderr.String())
			}
		})
	}
}

func TestCommandTreesAreIndependent(t *testing.T) {
	var firstOut, secondOut, stderr bytes.Buffer
	first := NewRootCommand(testDependencies(t, &firstOut, &stderr))
	second := NewRootCommand(testDependencies(t, &secondOut, &stderr))
	first.SetArgs([]string{"version", "--json"})
	second.SetArgs([]string{"version"})
	for _, root := range []*cobra.Command{first, second} {
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	assertStreams(t, &firstOut, &stderr, "{\"schema_version\":1,\"command\":\"brine version\",\"ok\":true,\"data\":{\"version\":\"0.1.0-dev\"},\"error\":null}\n")
	assertStreams(t, &secondOut, &stderr, "brine 0.1.0-dev\n")
}

func TestParserErrorsLeaveStreamsEmpty(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"not-a-command"}, `unknown command "not-a-command" for "brine"`},
		{"unknown flag", []string{"version", "--not-a-flag"}, "Invalid command or arguments. Use --help for usage."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := NewRootCommand(testDependencies(t, &stdout, &stderr))
			root.SetArgs(tt.args)
			if err := root.Execute(); err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v; want %q", err, tt.want)
			}
			assertStreams(t, &stdout, &stderr, "")
		})
	}
}

func TestHelpUsesCommandOutput(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"doctor", "--help"}, {"help", "tui"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			root := NewRootCommand(testDependencies(t, &stdout, &stderr))
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
				t.Fatalf("stdout = %q, stderr = %q; want help only on stdout", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunTUIWithInjectedInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	if err := RunTUI(ctx, strings.NewReader("q"), &stdout); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Press q to quit.") {
		t.Fatalf("TUI did not use injected output: %q", stdout.String())
	}
}
