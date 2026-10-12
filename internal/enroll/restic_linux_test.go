//go:build linux

package enroll

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestResticProtectedVersionRequiresExactPinnedExecutableIdentity(t *testing.T) {
	expected := "restic " + restic.Version + " compiled with go1.26.4 on linux/" + runtime.GOARCH
	for _, output := range []localexec.Result{
		{Stdout: expected + "\n"},
		{Stdout: "restic 0.19.0 compiled with go1.26.4 on linux/" + runtime.GOARCH},
		{Stdout: expected, Truncated: true},
		{Stdout: expected + "\nextra output"},
	} {
		executor := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) { return output, nil }}
		h := &host{exec: executor}
		err := h.resticVersion(context.Background(), restic.ToolPath)
		if (err == nil) != (strings.TrimSpace(output.Stdout) == expected && !output.Truncated) {
			t.Fatal("unpinned version output admitted")
		}
		if len(executor.commands) != 1 {
			t.Fatal("unexpected tool calls")
		}
		command := executor.commands[0]
		if command.Path != restic.ToolPath || len(command.Args) != 1 || command.Args[0] != "version" || command.Mutation || command.Timeout != 5*time.Second || command.OutputLimit != 1024 || len(command.Env) != 0 {
			t.Fatal("version probe carried mutable or ambient command data")
		}
	}
}
