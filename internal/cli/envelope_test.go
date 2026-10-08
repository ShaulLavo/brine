package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
)

func TestMachineGoldens(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		exit int
	}{
		{"version", []string{"version", "--json", "--no-input"}, 0},
		{"doctor", []string{"--json", "doctor", "--no-input"}, 0},
		{"unknown-command", []string{"not-a-command", "--json"}, 2},
		{"unknown-flag", []string{"version", "--unknown", "--json"}, 2},
		{"tui-refusal", []string{"tui", "--json"}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := testDependencies(t, &out, &diagnostics)
			configureDoctorFixture(t, &deps)
			err := Execute(deps, tt.args)
			if got := result.ExitCode(err); got != tt.exit {
				t.Fatalf("exit = %d, want %d (%v)", got, tt.exit, err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", tt.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.ReplaceAll(string(golden), "PLATFORM", runtime.GOOS)
			if out.String() != want {
				t.Fatalf("stdout = %q; want %q", out.String(), want)
			}
			decoder := json.NewDecoder(&out)
			var envelope result.Envelope
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("extra output: %v", err)
			}
			if strings.Contains(want, "\x1b") || strings.Contains(diagnostics.String(), "\x1b") {
				t.Fatal("ANSI in machine streams")
			}
			if tt.exit == 0 && diagnostics.Len() != 0 {
				t.Fatalf("diagnostics = %q", diagnostics.String())
			}
		})
	}
}

func TestMachineBoundaries(t *testing.T) {
	for _, args := range [][]string{
		{"--json"}, {"--json", "--help"}, {"help", "version", "--json"},
		{"--unknown=synthetic-secret", "--json"}, {"--json=broken"},
		{"--json", "version", "synthetic-secret"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			_ = Execute(testDependencies(t, &out, &diagnostics), args)
			dec := json.NewDecoder(&out)
			var got result.Envelope
			if err := dec.Decode(&got); err != nil {
				t.Fatal(err)
			}
			if err := dec.Decode(new(any)); err != io.EOF {
				t.Fatalf("extra output: %v", err)
			}
			if strings.Contains(diagnostics.String(), "synthetic-secret") {
				t.Fatal("unsafe diagnostic")
			}
		})
	}
}

func TestErrorRedactionT23(t *testing.T) {
	secret := "synthetic-secret-token"
	for _, args := range [][]string{{secret, "--json"}, {"version", "--" + secret, "--json"}, {"--json", "version", secret}} {
		var out, diagnostics bytes.Buffer
		if err := Execute(testDependencies(t, &out, &diagnostics), args); err == nil {
			t.Fatal("wanted usage failure")
		}
		if strings.Contains(out.String()+diagnostics.String(), secret) {
			t.Fatal("argument leaked")
		}
	}
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	deps.RunTUI = func(context.Context, io.Reader, io.Writer) error {
		return errors.New("subprocess stderr: TOKEN=" + secret + "\x1b[31m")
	}
	if err := Execute(deps, []string{"tui"}); result.ExitCode(err) != 1 {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String()+diagnostics.String(), secret) || strings.Contains(diagnostics.String(), "\x1b") {
		t.Fatal("raw service output leaked")
	}
}

func TestCanceledMachineCommand(t *testing.T) {
	var out, diagnostics bytes.Buffer
	deps := testDependencies(t, &out, &diagnostics)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps.Context = ctx
	err := Execute(deps, []string{"version", "--json"})
	if result.ExitCode(err) != 130 {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(out.String(), `"code":"interrupted"`) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestHelpMachineOutput(t *testing.T) {
	for _, tt := range []struct {
		args []string
		exit int
	}{
		{[]string{"help", "version", "--json"}, 0},
		{[]string{"help", "synthetic-secret", "--json"}, 2},
	} {
		var out, diagnostics bytes.Buffer
		err := Execute(testDependencies(t, &out, &diagnostics), tt.args)
		if got := result.ExitCode(err); got != tt.exit {
			t.Fatalf("%v: exit = %d, want %d", tt.args, got, tt.exit)
		}
		if strings.Contains(out.String()+diagnostics.String(), "synthetic-secret") {
			t.Fatal("help argument leaked")
		}
	}
}

func TestExecuteNilArgsUsesInjectedHelp(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := Execute(testDependencies(t, &out, &diagnostics), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Usage:") || diagnostics.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", out.String(), diagnostics.String())
	}
}

func TestMachineWriteFailure(t *testing.T) {
	var diagnostics bytes.Buffer
	deps := testDependencies(t, failingWriter{errors.New("synthetic-secret")}, &diagnostics)
	err := Execute(deps, []string{"version", "--json"})
	if result.ExitCode(err) != 1 {
		t.Fatalf("error = %v", err)
	}
	if diagnostics.String() != "error: The operation failed.\n" {
		t.Fatalf("diagnostics = %q", diagnostics.String())
	}
}

func TestExplicitFalseAndFlagTerminator(t *testing.T) {
	for _, args := range [][]string{{"version", "--json=false"}, {"version", "--json", "--json=false"}, {"version", "--", "--json"}} {
		var out, diagnostics bytes.Buffer
		err := Execute(testDependencies(t, &out, &diagnostics), args)
		if args[len(args)-2] == "--" {
			if result.ExitCode(err) != 2 || out.Len() != 0 {
				t.Fatalf("terminator streams: %q, %v", out.String(), err)
			}
		} else if err != nil || out.String() != "brine 0.1.0-dev\n" {
			t.Fatalf("false mode: %q, %v", out.String(), err)
		}
	}
}

func TestCompletionArgumentUsage(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		for _, machine := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", shell, machine), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				args := []string{"completion", shell, "synthetic-secret"}
				if machine {
					args = append(args, "--json")
				}
				err := Execute(testDependencies(t, &out, &diagnostics), args)
				if result.ExitCode(err) != 2 {
					t.Fatalf("exit = %d, error = %v", result.ExitCode(err), err)
				}
				if strings.Contains(out.String()+diagnostics.String(), "synthetic-secret") {
					t.Fatal("unsafe argument output")
				}
				if machine {
					var response result.Envelope
					if err := json.Unmarshal(out.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if response.Command != "brine completion "+shell || response.Error.Code != result.InvalidUsage {
						t.Fatalf("response = %+v", response)
					}
				} else if out.Len() != 0 {
					t.Fatalf("stdout = %q", out.String())
				}
			})
		}
	}
}
