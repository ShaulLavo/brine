package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/result"
)

func TestBinaryModeMatrix(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "brine")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, command := range []string{"version", "doctor", "tui"} {
		for bits := 0; bits < 8; bits++ {
			t.Run(fmt.Sprintf("%s/%03b", command, bits), func(t *testing.T) {
				args := []string{command}
				for i, flag := range []string{"--json", "--jsonl", "--no-input"} {
					if bits&(1<<i) != 0 {
						args = append(args, flag)
					}
				}
				var out, diagnostics bytes.Buffer
				process := exec.CommandContext(ctx, binary, args...)
				process.Stdout, process.Stderr = &out, &diagnostics
				process.Env = append(os.Environ(), "PATH=", "TERM=xterm-256color", "CLICOLOR_FORCE=1", "NO_COLOR=")
				err := process.Run()
				exit := 0
				if err != nil {
					var status *exec.ExitError
					if !errors.As(err, &status) {
						t.Fatal(err)
					}
					exit = status.ExitCode()
				}
				wantExit := 0
				if command == "tui" || bits&3 == 3 {
					wantExit = 2
				}
				if exit != wantExit {
					t.Fatalf("exit = %d, want %d; stderr = %q", exit, wantExit, diagnostics.String())
				}
				if strings.Contains(out.String()+diagnostics.String(), "\x1b") {
					t.Fatal("ANSI in redirected streams")
				}
				if bits&3 == 0 {
					if command == "version" && out.String() != "brine "+cli.Version+"\n" {
						t.Fatalf("stdout = %q", out.String())
					}
					if command == "doctor" && !strings.Contains(out.String(), "Checking PATH only") {
						t.Fatalf("stdout = %q", out.String())
					}
					if command == "tui" && out.Len() != 0 {
						t.Fatalf("stdout = %q", out.String())
					}
				} else {
					var envelope result.Envelope
					if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
						t.Fatalf("stdout = %q: %v", out.String(), err)
					}
					if strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(out.String(), "\n") {
						t.Fatalf("not one JSON line: %q", out.String())
					}
					if envelope.SchemaVersion != 1 || envelope.Command != "brine "+command || envelope.OK != (exit == 0) {
						t.Fatalf("envelope = %+v", envelope)
					}
					if bits&3 == 3 && envelope.Error.Code != result.InvalidUsage {
						t.Fatalf("conflict = %+v", envelope)
					}
					if command == "version" && exit == 0 {
						data := envelope.Data.(map[string]any)
						if data["version"] != cli.Version {
							t.Fatalf("version data = %+v", data)
						}
					}
					if command == "doctor" && exit == 0 {
						data := envelope.Data.(map[string]any)
						checks, ok := data["checks"].([]any)
						if !ok || len(checks) != 5 || data["platform"] == nil {
							t.Fatalf("doctor data = %+v", data)
						}
						for _, check := range checks {
							fields := check.(map[string]any)
							if fields["name"] == nil || fields["available"] != false {
								t.Fatalf("doctor check = %+v", fields)
							}
						}
					}
				}
				if exit == 0 && diagnostics.Len() != 0 {
					t.Fatalf("stderr = %q", diagnostics.String())
				}
				if exit != 0 {
					code := result.TUITerminalRequired
					if bits&3 == 3 {
						code = result.InvalidUsage
					} else if bits != 0 {
						code = result.TUIInteractive
					}
					if want := "error: " + result.New(code, nil).Error() + "\n"; diagnostics.String() != want {
						t.Fatalf("stderr = %q, want %q", diagnostics.String(), want)
					}
				}
			})
		}
	}
}
