package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func TestInitTerminalFinalOutput(t *testing.T) {
	for _, mode := range []string{"short", "error"} {
		t.Run(mode, func(t *testing.T) { checkInitTerminalFinalOutput(t, mode) })
	}
}

func checkInitTerminalFinalOutput(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	dir := t.TempDir()
	var output io.Writer = &finalOutputProbe{short: true}
	want := io.ErrShortWrite
	if mode == "error" {
		output, want = initFailedOutput{}, io.ErrClosedPipe
	}
	var diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, output, &diagnostics)
	withTestTerminal(t, &deps)
	err := Execute(deps, append(initArgs(dir), "--yes"))
	raw, readErr := readInitTestFile(t, dir, initFileName)
	if readErr != nil || len(raw) == 0 {
		t.Fatal("spec not preserved", readErr)
	}
	if !errors.Is(err, want) {
		t.Errorf("final human terminal output: err=%v; want %v with spec preserved", err, want)
	}
	var retry bytes.Buffer
	deps = initNoHostDependencies(t, &retry, &diagnostics)
	if err := Execute(deps, append(initArgs(dir), "--yes", "--json")); err != nil || !bytes.Contains(retry.Bytes(), []byte(`"status":"unchanged"`)) {
		t.Fatalf("rerun after terminal output failure: %v, %s", err, retry.String())
	}
}

func TestInitDashDirectoryGuidance(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("-project", 0700); err != nil {
		t.Fatal(err)
	}
	var output, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &output, &diagnostics)
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) { return transport.Target{Name: "fixture"}, nil }
	args := append([]string{"init"}, initArgs(".")[2:]...)
	args = append(args, "--target", "fixture", "--yes", "--json", "--", "-project")
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data initResult `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || len(response.Data.NextCommands) != 1 {
		t.Fatal("invalid init result", err, output.String())
	}
	if response.Data.File != filepath.Join("-project", initFileName) {
		t.Fatal("file result changed", response.Data.File)
	}
	next := response.Data.NextCommands[0]
	calls := 0
	sentinel := errors.New("bounded transport reached")
	deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
		calls++
		return result.Envelope{}, sentinel
	})
	output.Reset()
	err := Execute(deps, strings.Fields(strings.TrimPrefix(next, "brine ")))
	if calls != 1 || !errors.Is(err, sentinel) {
		t.Fatalf("emitted command %q: err=%v calls=%d output=%s", next, err, calls, output.String())
	}
}
