package udpecho

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFixtureCommandsForbidDownloadsBeforeFirstGoCall(t *testing.T) {
	for _, script := range []string{"integration/udp-delivery/run-local.sh", "fixtures/udp-echo/build.sh"} {
		t.Run(script, func(t *testing.T) { checkDownloadBoundary(t, script) })
	}
}

func checkDownloadBoundary(t *testing.T, script string) {
	t.Helper()
	var pins struct {
		GoVersion string `json:"go_version"`
	}
	if err := json.Unmarshal(readFile(t, "fixtures/udp-echo/pins.json"), &pins); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	files, err := os.OpenRoot(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	if err := files.MkdirAll(".local/share/mise/shims", 0700); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteFile(".local/share/mise/shims/go", []byte(downloadBoundaryGo), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	arguments := `set -- "$UDP_OUTPUT"`
	if strings.HasSuffix(script, "build.sh") {
		arguments += " release-control"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-s")
	cmd.Stdin = strings.NewReader("BASH_ARGV0=\"$UDP_SCRIPT\"\n" + arguments + "\n" + string(readFile(t, script)))
	log := filepath.Join(tmp, "go-calls")
	cmd.Env = append(os.Environ(), "HOME="+tmp, "TMPDIR="+tmp, "GOTOOLCHAIN=auto", "GOPROXY=https://invalid.example", "GOSUMDB=sum.golang.org", "GONOPROXY=*", "UDP_GO_VERSION="+pins.GoVersion, "UDP_GO_LOG="+log, "UDP_SCRIPT="+filepath.Join(root, script), "UDP_OUTPUT="+filepath.Join(tmp, "output"))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 77 {
		t.Fatalf("runner did not reach guarded stopping point: %v\n%s", err, out)
	}
	calls, err := files.ReadFile("go-calls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "-mod=readonly") {
		t.Fatal("test, vet and build require explicit read-only module mode")
	}
}

const downloadBoundaryGo = `#!/usr/bin/env bash
set -euo pipefail
[[ $GOTOOLCHAIN == local && $GOPROXY == off && $GOSUMDB == off && $GONOPROXY == none ]] || exit 71
printf '%s\n' "$*" >> "$UDP_GO_LOG"
case "$1" in
  version) printf 'go version go%s linux/amd64\n' "$UDP_GO_VERSION" ;;
  env) [[ $2 == GOVERSION ]]; printf 'go%s\n' "$UDP_GO_VERSION" ;;
  test) [[ " $* " == *" -mod=readonly "* ]] || exit 72 ;;
  vet|build) [[ " $* " == *" -mod=readonly "* ]] || exit 73; exit 77 ;;
  *) exit 74 ;;
esac
`
