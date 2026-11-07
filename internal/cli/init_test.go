package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

func initArgs(dir string) []string {
	return []string{"init", dir, "--name", "hello", "--image", "ghcr.io/example/hello@sha256:" + strings.Repeat("a", 64), "--container-port", "3000", "--domain", "Hello.Example.com"}
}

func TestInitCommandModes(t *testing.T) {
	t.Run("refusals", checkInitCommandRefusals)
	t.Run("selected pin and stable output", checkInitTargetPinAndDeterministicPreview)
	t.Run("terminal machine preview", checkInitTerminalMachinePreview)
	t.Run("default existing directory", checkInitDefaultDirectory)
	t.Run("missing directory", checkInitMissingDirectory)
	for _, flags := range [][]string{nil, {"--no-input"}, {"--json"}, {"--jsonl"}, {"--yes"}, {"--yes", "--json"}, {"--yes", "--jsonl"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) { checkInitMode(t, flags) })
	}
}

func checkInitMode(t *testing.T, flags []string) {
	t.Helper()
	dir := t.TempDir()
	var out, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &out, &diagnostics)
	if err := Execute(deps, append(initArgs(dir), flags...)); err != nil {
		t.Fatal(err)
	}
	created := strings.Contains(strings.Join(flags, " "), "--yes")
	raw, err := readInitTestFile(t, dir, initFileName)
	if created && (err != nil || !bytes.Contains(raw, []byte("hello.example.com"))) {
		t.Fatalf("created file = %s, %v", raw, err)
	}
	if !created && !os.IsNotExist(err) {
		t.Fatalf("preview wrote file: %v", err)
	}
	if diagnostics.Len() != 0 {
		t.Fatal(diagnostics.String())
	}
	if !strings.Contains(strings.Join(flags, " "), "--json") {
		return
	}
	var envelope result.Envelope
	if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("envelope = %s", out.String())
	}
}

func checkInitCommandRefusals(t *testing.T) {
	for _, flags := range [][]string{
		{"--name", ""}, {"--image", "ghcr.io/example/hello:latest"}, {"--image", "ghcr.io/example/hello@sha256:bad"},
		{"--container-port", "0"}, {"--container-port", "65536"}, {"--domain", ""}, {"--health-path", ""},
		{"--target", ""}, {"--target", "../bad"}, {"--config-dir", "bad\x1bpath"}, {"extra"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) { checkInitRefusal(t, flags) })
	}
	for _, flag := range []string{"--name", "--image", "--container-port", "--domain"} {
		t.Run("missing "+flag, func(t *testing.T) { checkInitMissing(t, flag) })
	}
}

func checkInitRefusal(t *testing.T, flags []string) {
	t.Helper()
	dir := t.TempDir()
	var out, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &out, &diagnostics)
	err := Execute(deps, append(append(initArgs(dir), flags...), "--json"))
	if result.ExitCode(err) != 2 {
		t.Fatalf("refusal = %v, %s", err, out.String())
	}
	assertInitFiles(t, dir, nil)
	var response result.Envelope
	if json.Unmarshal(out.Bytes(), &response) != nil || response.OK || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("refusal envelope = %s", out.String())
	}
}

func checkInitMissing(t *testing.T, missing string) {
	t.Helper()
	dir := t.TempDir()
	args := initArgs(dir)
	for i, arg := range args {
		if arg != missing {
			continue
		}
		args = append(args[:i:i], args[i+2:]...)
		break
	}
	var out, diagnostics bytes.Buffer
	if err := Execute(initNoHostDependencies(t, &out, &diagnostics), args); result.ExitCode(err) != 2 {
		t.Fatalf("missing input = %v", err)
	}
	assertInitFiles(t, dir, nil)
}

func initNoHostDependencies(t *testing.T, stdout, stderr io.Writer) Dependencies {
	t.Helper()
	deps := testDependencies(t, stdout, stderr)
	deps.OperationClient = callFunc(func(context.Context, transport.Target, dispatch.Request) (result.Envelope, error) {
		t.Fatal("init contacted a host")
		return result.Envelope{}, nil
	})
	deps.LoadOperationTarget = func(string, string) (transport.Target, error) {
		t.Fatal("target-neutral init accessed target configuration")
		return transport.Target{}, nil
	}
	return deps
}

func assertInitFiles(t *testing.T, dir string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func checkInitTargetPinAndDeterministicPreview(t *testing.T) {
	dir := t.TempDir()
	config := t.TempDir()
	var out, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &out, &diagnostics)
	calls := 0
	deps.LoadOperationTarget = func(directory, name string) (transport.Target, error) {
		calls++
		if directory != config || name != "fixture" {
			t.Fatalf("selection = %q %q", directory, name)
		}
		return transport.Target{Name: name}, nil
	}
	args := append(initArgs(dir), "--target", "fixture", "--config-dir", config, "--json")
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	out.Reset()
	if err := Execute(deps, args); err != nil {
		t.Fatal(err)
	}
	if out.String() != first || calls != 2 {
		t.Fatalf("preview unstable, calls = %d", calls)
	}
	var envelope struct {
		Data initResult `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(envelope.Data.Content, "target") || envelope.Data.Target != "fixture" || len(envelope.Data.NextCommands) != 1 {
		t.Fatalf("result = %+v", envelope)
	}
	assertInitFiles(t, dir, nil)
	assertInitFiles(t, config, nil)
}

func checkInitTerminalMachinePreview(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"--json", "--jsonl", "--no-input"} {
		t.Run(mode, func(t *testing.T) { checkInitTerminalMode(t, mode) })
	}
}

func checkInitTerminalMode(t *testing.T, mode string) {
	t.Helper()
	dir := t.TempDir()
	var out, diagnostics bytes.Buffer
	deps := initNoHostDependencies(t, &out, &diagnostics)
	withTestTerminal(t, &deps)
	if err := Execute(deps, append(initArgs(dir), mode)); err != nil {
		t.Fatal(err)
	}
	assertInitFiles(t, dir, nil)
}

func checkInitDefaultDirectory(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	var out, diagnostics bytes.Buffer
	args := append([]string{"init"}, initArgs(".")[2:]...)
	if err := Execute(initNoHostDependencies(t, &out, &diagnostics), append(args, "--yes")); err != nil {
		t.Fatal(err)
	}
	assertInitFiles(t, dir, []string{initFileName})
}

func checkInitMissingDirectory(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	var out, diagnostics bytes.Buffer
	if err := Execute(initNoHostDependencies(t, &out, &diagnostics), append(initArgs(dir+"/missing"), "--yes")); err == nil {
		t.Fatal("created a missing project directory")
	}
	assertInitFiles(t, dir, nil)
}
