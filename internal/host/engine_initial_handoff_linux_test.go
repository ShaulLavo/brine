//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestInitialEngineHandoffNeverRebindsForeignContainerOrPID(t *testing.T) {
	for _, kind := range []string{"cid", "pid", "image", "mount"} {
		t.Run(kind, func(t *testing.T) { testInitialEngineForeignHandoff(t, kind) })
	}
}

func testInitialEngineForeignHandoff(t *testing.T, kind string) {
	t.Helper()
	container, permit, material := ownedEngineFixture(t)
	container.State.CgroupPath = "/user.slice/user-" + strconv.Itoa(os.Geteuid()) + ".slice/user@" + strconv.Itoa(os.Geteuid()) + ".service/user.slice/libpod-" + container.ID + ".scope"
	switch kind {
	case "cid":
		container.ID = strings.Repeat("d", 64)
	case "pid":
		*container.State.PID = os.Getpid()
	case "image":
		container.Image = strings.Repeat("d", 64)
	case "mount":
		container.Mounts[0].Source = "/foreign"
	}
	raw, err := json.Marshal([]engineContainer{container})
	if err != nil {
		t.Fatal(err)
	}
	runner := &engineInspectRunner{results: []localexec.Result{{Stdout: string(raw)}, {Stdout: string(raw)}}}
	session, err := localexec.NewSession(runner, uint32(os.Geteuid()), t.TempDir(), time.Second) //nolint:gosec // Linux UID originates as an unsigned syscall identity.
	if err != nil {
		t.Fatal(err)
	}
	runtime := EngineStartupRuntime{session: session}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if handle, err := runtime.waitEngineHandoff(ctx, permit, material, make(chan error)); err == nil || handle != nil {
		t.Fatal("foreign startup candidate granted a native handle", kind, err)
	}
	if len(runner.commands) != 1 || runner.commands[0].Mutation {
		t.Fatal("foreign startup was retried, replaced or changed", kind)
	}
	if handle, err := bindEngineNative(context.Background(), session, container, permit, material); err == nil || handle != nil {
		t.Fatal("routine live binding gained startup acceptance", kind)
	}
}
