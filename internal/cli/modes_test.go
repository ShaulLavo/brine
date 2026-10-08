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
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

func TestModeFlagMatrix(t *testing.T) {
	for _, command := range []string{"version", "doctor", "tui"} {
		for bits := 0; bits < 8; bits++ {
			t.Run(fmt.Sprintf("%s/%03b", command, bits), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				deps := testDependencies(t, &out, &diagnostics)
				configureDoctorFixture(t, &deps)
				args := []string{command}
				for i, flag := range []string{"--json", "--jsonl", "--no-input"} {
					if bits&(1<<i) != 0 {
						args = append(args, flag)
					}
				}
				wantExit := 0
				if bits&3 == 3 || command == "tui" {
					wantExit = 2
				}
				err := Execute(deps, args)
				if result.ExitCode(err) != wantExit {
					t.Fatalf("exit = %d (%v), want %d", result.ExitCode(err), err, wantExit)
				}
				if bits&3 != 0 {
					var envelope result.Envelope
					if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
						t.Fatalf("stdout = %q: %v", out.String(), err)
					}
					if strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(out.String(), "\n") {
						t.Fatalf("not one JSON line: %q", out.String())
					}
					if envelope.SchemaVersion != 1 || envelope.Command != "brine "+command || envelope.OK != (wantExit == 0) {
						t.Fatalf("envelope = %+v", envelope)
					}
					if bits&3 == 3 && envelope.Error.Code != result.InvalidUsage {
						t.Fatalf("conflicting flags = %+v", envelope)
					}
				} else if command == "tui" && out.Len() != 0 {
					t.Fatalf("stdout = %q", out.String())
				}
				if wantExit == 0 && diagnostics.Len() != 0 {
					t.Fatalf("stderr = %q", diagnostics.String())
				}
				if wantExit != 0 && diagnostics.String() != "error: "+err.Error()+"\n" {
					t.Fatalf("stderr = %q", diagnostics.String())
				}
				if strings.Contains(out.String()+diagnostics.String(), "\x1b") {
					t.Fatal("ANSI in nonterminal streams")
				}
			})
		}
	}
}

func TestJSONLInvocationBoundaries(t *testing.T) {
	for _, tt := range []struct {
		args    []string
		exit    int
		machine bool
	}{
		{[]string{"version", "--jsonl"}, 0, true},
		{[]string{"version", "--jsonl", "--jsonl=false"}, 0, false},
		{[]string{"version", "--json=true", "--jsonl=false"}, 0, true},
		{[]string{"version", "--json=false", "--jsonl=true"}, 0, true},
		{[]string{"version", "--", "--jsonl"}, 2, false},
		{[]string{"--jsonl=broken", "version"}, 2, true},
		{[]string{"--unknown=synthetic-secret", "--jsonl"}, 2, true},
		{[]string{"unknown", "--jsonl"}, 2, true},
		{[]string{"--jsonl", "--help"}, 0, true},
		{[]string{"help", "version", "--jsonl"}, 0, true},
		{[]string{"--json", "--jsonl", "--help"}, 2, true},
		{[]string{"help", "version", "--json", "--jsonl"}, 2, true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := Execute(testDependencies(t, &out, &diagnostics), tt.args)
			if result.ExitCode(err) != tt.exit {
				t.Fatalf("exit = %d (%v), want %d", result.ExitCode(err), err, tt.exit)
			}
			if tt.machine {
				var response result.Envelope
				if err := json.Unmarshal(out.Bytes(), &response); err != nil {
					t.Fatalf("stdout = %q: %v", out.String(), err)
				}
				if strings.Count(out.String(), "\n") != 1 {
					t.Fatalf("not one line: %q", out.String())
				}
			} else if tt.exit == 0 && out.String() != "brine 0.1.0-dev\n" {
				t.Fatalf("stdout = %q", out.String())
			}
			if strings.Contains(out.String()+diagnostics.String(), "synthetic-secret") {
				t.Fatal("argument leaked")
			}
		})
	}
}

type terminalReader struct {
	io.Reader
	fd uintptr
}

func (r terminalReader) Fd() uintptr { return r.fd }

type terminalWriter struct {
	io.Writer
	fd uintptr
}

func (w terminalWriter) Fd() uintptr { return w.fd }

func withTestTerminal(t *testing.T, deps *Dependencies) {
	t.Helper()
	terminal, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skip("PTY unavailable", err)
	}
	t.Cleanup(func() { terminal.Close() })
	deps.Stdin = terminalReader{deps.Stdin, terminal.Fd()}
	deps.Stdout = terminalWriter{deps.Stdout, terminal.Fd()}
}

func TestRequestInputPolicy(t *testing.T) {
	for _, tt := range []struct {
		flags   []string
		tty     bool
		refused bool
	}{
		{nil, true, false}, {nil, false, true},
		{[]string{"--no-input"}, true, true}, {[]string{"--json"}, true, true},
		{[]string{"--jsonl"}, true, true}, {[]string{"--no-input=false"}, true, false},
	} {
		t.Run(fmt.Sprintf("%v/tty=%t", tt.flags, tt.tty), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := testDependencies(t, &out, &diagnostics)
			if tt.tty {
				withTestTerminal(t, &deps)
			}
			root := NewRootCommand(deps)
			called := false
			root.AddCommand(&cobra.Command{Use: "ask", RunE: func(cmd *cobra.Command, _ []string) error {
				return RequestInput(cmd, func(ctx context.Context, stdin io.Reader, stderr io.Writer) error {
					called = true
					if ctx != deps.Context || stdin != deps.Stdin || stderr != deps.Stderr {
						t.Fatal("prompt lost injected resources")
					}
					fmt.Fprint(stderr, "fixture prompt")
					return nil
				})
			}})
			root.SetArgs(append([]string{"ask"}, tt.flags...))
			err := root.Execute()
			if tt.refused {
				if result.Classify(err) == nil || result.Classify(err).Code() != result.InputRequired || result.ExitCode(err) != 2 {
					t.Fatalf("error = %v", err)
				}
				if called || out.Len() != 0 || diagnostics.Len() != 0 {
					t.Fatal("refusal emitted a prompt")
				}
			} else if err != nil || !called || diagnostics.String() != "fixture prompt" {
				t.Fatalf("prompt = %t, stderr = %q, error = %v", called, diagnostics.String(), err)
			}
		})
	}
}

func TestHumanTerminalOutputPolicy(t *testing.T) {
	for _, tt := range []struct {
		tty     bool
		noColor string
		styled  bool
	}{
		{false, "", false}, {true, "", true}, {true, "1", false}, {false, "1", false},
	} {
		t.Run(fmt.Sprintf("tty=%t/NO_COLOR=%s", tt.tty, tt.noColor), func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.noColor)
			t.Setenv("CLICOLOR_FORCE", "1")
			var out, diagnostics bytes.Buffer
			deps := testDependencies(t, &out, &diagnostics)
			if tt.tty {
				withTestTerminal(t, &deps)
			}
			deps.Version = "\x1b[31mstyled\x1b[0m"
			if err := Execute(deps, []string{"version"}); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(out.String(), "\x1b"); got != tt.styled {
				t.Fatalf("stdout = %q, styled = %t", out.String(), tt.styled)
			}
			if !tt.styled && out.String() != "brine styled\n" {
				t.Fatalf("stdout = %q", out.String())
			}
		})
	}
}

func TestPlainWriterSplitSequences(t *testing.T) {
	var out bytes.Buffer
	w := newPlainWriter(&out)
	for _, text := range []string{"\x1b[", "31mred", "\x1b[0m ", "\xe2", "\x98\x83", "\x1b]8;;https://example.invalid", "\x1b\\link\x1b]8;;\x1b", "\\\n"} {
		if n, err := io.WriteString(w, text); err != nil || n != len(text) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if out.String() != "red ☃link\n" {
		t.Fatalf("stdout = %q", out.String())
	}
	if _, err := newPlainWriter(failingWriter{io.ErrClosedPipe}).Write([]byte("text")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error = %v", err)
	}
}

func TestCompletionKeepsJSONLProtocol(t *testing.T) {
	for _, command := range []string{"__complete", "__completeNoDesc"} {
		var out, diagnostics bytes.Buffer
		if err := Execute(testDependencies(t, &out, &diagnostics), []string{command, "version", "--jsonl", ""}); err != nil {
			t.Fatal(err)
		}
		if json.Valid(out.Bytes()) || !strings.Contains(out.String(), ":") {
			t.Fatalf("completion stdout = %q", out.String())
		}
	}
}

func TestTUITerminalMatrix(t *testing.T) {
	for bits := 0; bits < 4; bits++ {
		t.Run(fmt.Sprintf("stdin/stdout=%02b", bits), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps := testDependencies(t, &out, &diagnostics)
			input, output := deps.Stdin, deps.Stdout
			withTestTerminal(t, &deps)
			if bits&1 == 0 {
				deps.Stdin = input
			}
			if bits&2 == 0 {
				deps.Stdout = output
			}
			called := false
			deps.RunTUI = func(context.Context, io.Reader, io.Writer) error { called = true; return nil }
			err := Execute(deps, []string{"tui"})
			if bits == 3 {
				if err != nil || !called {
					t.Fatalf("runner = %t, error = %v", called, err)
				}
			} else {
				if called || result.ExitCode(err) != 2 || result.Classify(err).Code() != result.TUITerminalRequired {
					t.Fatalf("runner = %t, error = %v", called, err)
				}
				if out.Len() != 0 || diagnostics.String() != "error: "+err.Error()+"\n" {
					t.Fatalf("stdout = %q, stderr = %q", out.String(), diagnostics.String())
				}
			}
		})
	}
}

func TestJSONLGoldens(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		exit int
	}{
		{"modes-version.jsonl", []string{"version", "--jsonl", "--no-input"}, 0},
		{"modes-conflict.jsonl", []string{"version", "--json", "--jsonl"}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := Execute(testDependencies(t, &out, &diagnostics), tt.args)
			if result.ExitCode(err) != tt.exit {
				t.Fatalf("error = %v", err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", tt.name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("stdout = %q, want %q", out.String(), want)
			}
		})
	}
}

func TestJSONLFailurePresentation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		var diagnostics bytes.Buffer
		deps := testDependencies(t, failingWriter{io.ErrClosedPipe}, &diagnostics)
		var out bytes.Buffer
		if canceled {
			ctx, cancel := context.WithCancel(deps.Context)
			cancel()
			deps.Context, deps.Stdout = ctx, &out
		}
		err := Execute(deps, []string{"version", "--jsonl"})
		want := 1
		if canceled {
			want = 130
		}
		if result.ExitCode(err) != want {
			t.Fatalf("exit = %d, want %d", result.ExitCode(err), want)
		}
		if diagnostics.String() != "error: "+err.Error()+"\n" {
			t.Fatalf("stderr = %q", diagnostics.String())
		}
		if canceled && !strings.Contains(out.String(), `"code":"interrupted"`) {
			t.Fatalf("stdout = %q", out.String())
		}
	}
}

func TestMachineOutputOnTerminals(t *testing.T) {
	for _, flag := range []string{"--json", "--jsonl"} {
		for _, noColor := range []string{"", "1"} {
			t.Run(flag+"/NO_COLOR="+noColor, func(t *testing.T) {
				t.Setenv("NO_COLOR", noColor)
				t.Setenv("CLICOLOR_FORCE", "1")
				var out, diagnostics bytes.Buffer
				deps := testDependencies(t, &out, &diagnostics)
				withTestTerminal(t, &deps)
				if err := Execute(deps, []string{"version", flag}); err != nil {
					t.Fatal(err)
				}
				if !json.Valid(out.Bytes()) || strings.Contains(out.String()+diagnostics.String(), "\x1b") || strings.Count(out.String(), "\n") != 1 {
					t.Fatalf("stdout = %q, stderr = %q", out.String(), diagnostics.String())
				}
			})
		}
	}
}

func TestMalformedModeValueSurvivesFalse(t *testing.T) {
	for _, flag := range []string{"--json", "--jsonl"} {
		for _, values := range [][]string{{flag + "=broken", flag + "=false"}, {flag + "=false", flag + "=broken", flag + "=false"}} {
			t.Run(strings.Join(values, " "), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				err := Execute(testDependencies(t, &out, &diagnostics), append([]string{"version"}, values...))
				if result.ExitCode(err) != 2 {
					t.Fatalf("exit = %d, want 2", result.ExitCode(err))
				}
				var response result.Envelope
				if err := json.Unmarshal(out.Bytes(), &response); err != nil {
					t.Fatalf("stdout = %q: %v", out.String(), err)
				}
				if response.OK || response.Error == nil || response.Error.Code != result.InvalidUsage || strings.Count(out.String(), "\n") != 1 {
					t.Fatalf("stdout = %q", out.String())
				}
				if diagnostics.String() != "error: "+result.New(result.InvalidUsage, nil).Error()+"\n" {
					t.Fatalf("stderr = %q", diagnostics.String())
				}
			})
		}
	}
}

func TestPlainWriterPreservesMalformedUTF8(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"before\xe2BCafter", "before\xe2BCafter"},
		{"before\xe2\x1b[31mBC\x1b[0mafter", "before\xe2BCafter"},
		{"before\xf0\x9fBafter", "before\xf0\x9fBafter"},
		{"before\xff\xc0Bafter", "before\xff\xc0Bafter"},
		{"before\xe2", "before\xe2"},
		{"before\xe2\x98\x83after", "before☃after"},
		{"before\xe2\x98\x83\x9b31mafter\x9b0m", "before☃after"},
		{"before\xe2\x1b]0;title\x07after", "before\xe2after"},
		{"before\xf0\x9f\x1bPdata\x1b\\after", "before\xf0\x9fafter"},
		{"before\x9d0;title\x9cafter", "beforeafter"},
		{"before\x90data\x9cafter", "beforeafter"},
		{"\x1b[\xe2BCafter", "\xe2BCafter"},
		{"a\v\f\b\ab\n\r\t", "ab\n\r\t"},
	} {
		for chunk := 1; chunk <= len(tt.input); chunk++ {
			t.Run(fmt.Sprintf("%x/chunk=%d", tt.input, chunk), func(t *testing.T) {
				var out bytes.Buffer
				w := newPlainWriter(&out)
				for offset := 0; offset < len(tt.input); offset += chunk {
					end := min(offset+chunk, len(tt.input))
					if _, err := io.WriteString(w, tt.input[offset:end]); err != nil {
						t.Fatal(err)
					}
				}
				if out.String() != tt.want {
					t.Fatalf("stdout = %q, want %q", out.String(), tt.want)
				}
			})
		}
	}
}

func TestHumanThemeOutputPolicy(t *testing.T) {
	for _, tty := range []bool{false, true} {
		for _, noColor := range []string{"", "1"} {
			t.Run(fmt.Sprintf("tty=%t/NO_COLOR=%s", tty, noColor), func(t *testing.T) {
				t.Setenv("NO_COLOR", noColor)
				t.Setenv("CLICOLOR_FORCE", "1")
				var out, diagnostics bytes.Buffer
				deps := testDependencies(t, &out, &diagnostics)
				if tty {
					withTestTerminal(t, &deps)
				}
				text := humanTheme(deps.Stdout).Title.Render("title")
				if got, want := strings.Contains(text, "\x1b"), tty && noColor == ""; got != want {
					t.Fatalf("rendered = %q, styled = %t", text, want)
				}
			})
		}
	}
}
