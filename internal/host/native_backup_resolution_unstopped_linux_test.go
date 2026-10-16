//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/store"
)

type unstoppedFrontendExecutor struct {
	properties map[string]string
	container  *podman.ContainerState
}

func (e unstoppedFrontendExecutor) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Mutation {
		return localexec.Result{}, errors.New("frontend inspection attempted mutation")
	}
	if filepath.Base(c.Path) == "systemctl" && len(c.Args) == 4 && strings.Join(c.Args[:3], " ") == "--user show game.service" {
		return e.show(c.Args[3])
	}
	if filepath.Base(c.Path) != "podman" {
		return localexec.Result{}, errors.New("unexpected frontend inspection")
	}
	switch strings.Join(c.Args, " ") {
	case "container exists systemd-game":
		if e.container == nil {
			return localexec.Result{ExitCode: 1}, &localexec.Error{Kind: localexec.Failed, ExitCode: 1}
		}
		return localexec.Result{}, nil
	case "container inspect systemd-game":
		raw, err := json.Marshal([]map[string]any{{"State": e.container}})
		return localexec.Result{Stdout: string(raw)}, err
	default:
		return localexec.Result{}, errors.New("unexpected frontend container inspection")
	}
}

func (e unstoppedFrontendExecutor) show(propertyArg string) (localexec.Result, error) {
	properties, ok := strings.CutPrefix(propertyArg, "--property=")
	if !ok {
		return localexec.Result{}, errors.New("unexpected frontend unit inspection")
	}
	var output strings.Builder
	for _, key := range strings.Split(properties, ",") {
		value, ok := e.properties[key]
		if !ok {
			return localexec.Result{}, errors.New("unexpected frontend unit property")
		}
		output.WriteString(key + "=" + value + "\n")
	}
	return localexec.Result{Stdout: output.String()}, nil
}

type unstoppedFrontendCase struct {
	name            string
	properties      map[string]string
	container       *podman.ContainerState
	frontendStopped bool
	wantRunning     bool
	wantError       bool
}

func TestNativeUnstoppedFrontend(t *testing.T) {
	stopped := &podman.ContainerState{Status: "exited"}
	running := &podman.ContainerState{Status: "running", Running: true}
	for _, test := range []unstoppedFrontendCase{
		{name: "inactive_absent"},
		{name: "inactive_container_stopped", container: stopped},
		{name: "failed_absent", properties: map[string]string{"ActiveState": "failed", "SubState": "failed"}},
		{name: "failed_container_stopped", properties: map[string]string{"ActiveState": "failed", "SubState": "failed"}, container: stopped, frontendStopped: true},
		{name: "main_pid", properties: map[string]string{"MainPID": "42"}, wantError: true},
		{name: "control_pid", properties: map[string]string{"ControlPID": "43"}, wantError: true},
		{name: "cgroup", properties: map[string]string{"ControlGroup": "/user.slice/game.service"}, wantError: true},
		{name: "pending_job", properties: map[string]string{"Job": "7"}, wantError: true},
		{name: "inactive_container_running", container: running, wantError: true},
		{name: "running", properties: map[string]string{"ActiveState": "active", "SubState": "running", "MainPID": "42", "ControlGroup": "/user.slice/game.service"}, container: running, wantRunning: true},
		{name: "running_after_recorded_stop", properties: map[string]string{"ActiveState": "active", "SubState": "running"}, container: running, frontendStopped: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) { checkUnstoppedFrontend(t, test) })
	}
}

func checkUnstoppedFrontend(t *testing.T, test unstoppedFrontendCase) {
	t.Helper()
	properties := map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead", "NRestarts": "0", "ActiveEnterTimestampMonotonic": "0", "MainPID": "0", "ControlPID": "0", "ControlGroup": "", "Job": ""}
	for key, value := range test.properties {
		properties[key] = value
	}
	executor := unstoppedFrontendExecutor{properties: properties, container: test.container}
	session, err := localexec.NewSession(executor, nativeBackupFixtureUID(t), t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	operation := nativeBackupResolutionOperations{runtime: nativeBackupRuntime{session: session}}
	expected := store.NativeBackupPreparedExpectation{FrontendStopped: test.frontendStopped, Release: store.Release{Managed: &ops.ManagedRelease{Frontend: &ops.ManagedFrontend{}}}}
	expected.Routes.App = "game"
	running, err := operation.inspectUnstoppedFrontend(t.Context(), expected)
	if (err != nil) != test.wantError || running != test.wantRunning {
		t.Fatalf("frontend running=%v err=%v; want running=%v error=%v", running, err, test.wantRunning, test.wantError)
	}
}
