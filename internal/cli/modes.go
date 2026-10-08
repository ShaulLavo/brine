package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

type machineModes struct {
	json, jsonl bool
}

func (m machineModes) enabled() bool { return m.json || m.jsonl }

// Inspect flags before Cobra can stop at an earlier parser error. Completion
// requests carry another command line, not flags for the current invocation.
func requestedModes(args []string) machineModes {
	var modes machineModes
	if len(args) > 0 && (args[0] == cobra.ShellCompRequestCmd || args[0] == cobra.ShellCompNoDescRequestCmd) {
		return modes
	}
	flags := []struct {
		name    string
		enabled *bool
	}{{"json", &modes.json}, {"jsonl", &modes.jsonl}}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		for _, flag := range flags {
			name, enabled := flag.name, flag.enabled
			if arg == "--"+name {
				*enabled = true
			} else if value, found := strings.CutPrefix(arg, "--"+name+"="); found {
				parsed, err := strconv.ParseBool(value)
				*enabled = parsed || err != nil
			}
		}
	}
	return modes
}

func isTerminal(stream any) bool {
	file, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(file.Fd())
}

// Only the process's own stdin may fall back to its controlling terminal.
// Explicitly injected input must never be replaced with a host input device.
func hasProcessTerminalFallback(stdin io.Reader) bool {
	if stdin != os.Stdin {
		return false
	}
	terminal, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer terminal.Close()
	return isTerminal(terminal)
}

// RequestInput is the only entry point commands may use to prompt. The callback
// owns the prompt and answer, and runs only after the interaction policy permits it.
func RequestInput(cmd *cobra.Command, prompt func(context.Context, io.Reader, io.Writer) error) error {
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	for _, flag := range []string{"no-input", "json", "jsonl"} {
		enabled, err := cmd.Flags().GetBool(flag)
		if err != nil {
			return result.New(result.InvalidUsage, err)
		}
		if enabled {
			return result.New(result.InputRequired, nil)
		}
	}
	if !isTerminal(cmd.InOrStdin()) || !isTerminal(cmd.OutOrStdout()) {
		return result.New(result.InputRequired, nil)
	}
	return prompt(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr())
}

func humanOutput(stdout io.Writer) io.Writer {
	if isTerminal(stdout) && os.Getenv("NO_COLOR") == "" {
		return stdout
	}
	return newPlainWriter(stdout)
}

type plainWriter struct {
	output io.Writer
	parser *ansi.Parser
	text   bytes.Buffer
}

func newPlainWriter(output io.Writer) *plainWriter {
	w := &plainWriter{output: output, parser: ansi.NewParser()}
	w.parser.SetDataSize(1)
	w.parser.SetHandler(ansi.Handler{
		Print: func(r rune) { w.text.WriteRune(r) },
		Execute: func(b byte) {
			if b == '\n' || b == '\r' || b == '\t' {
				w.text.WriteByte(b)
			}
		},
	})
	return w
}

func (w *plainWriter) Write(p []byte) (int, error) {
	w.text.Reset()
	w.parser.Parse(p)
	if _, err := io.Copy(w.output, &w.text); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *plainWriter) Fd() uintptr {
	if file, ok := w.output.(interface{ Fd() uintptr }); ok {
		return file.Fd()
	}
	return ^uintptr(0)
}
