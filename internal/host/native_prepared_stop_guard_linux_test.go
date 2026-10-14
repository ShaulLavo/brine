//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
)

type preparedStopExecutor struct {
	container []byte
	id        string
	active    bool
	commands  []localexec.Command
}

func (e *preparedStopExecutor) Execute(_ context.Context, command localexec.Command) (localexec.Result, error) {
	e.commands = append(e.commands, command)
	if command.Mutation {
		return localexec.Result{}, errors.New("stopped inspection attempted mutation")
	}
	if command.Path == "/usr/bin/systemctl" {
		state := "inactive"
		if e.active {
			state = "active"
		}
		return localexec.Result{Stdout: "LoadState=loaded\nActiveState=" + state + "\nSubState=dead\nMainPID=0\nControlPID=0\nControlGroup=\nJob=\n"}, nil
	}
	if command.Path == "/usr/bin/podman" && len(command.Args) > 0 && command.Args[0] == "ps" {
		return localexec.Result{Stdout: e.id}, nil
	}
	if command.Path == "/usr/bin/podman" && len(command.Args) == 3 && command.Args[0] == "container" && command.Args[1] == "inspect" && command.Args[2] == e.id {
		return localexec.Result{Stdout: string(e.container)}, nil
	}
	return localexec.Result{}, errors.New("unexpected stopped inspection")
}

type preparedStopRoutes struct{}

func (preparedStopRoutes) Validate(context.Context, string) error {
	return errors.New("stopped inspection attempted validation")
}
func (preparedStopRoutes) Adapt(context.Context, string) ([]byte, error) {
	return nil, errors.New("stopped inspection attempted adaptation")
}
func (preparedStopRoutes) Reload(context.Context) error {
	return errors.New("stopped inspection attempted reload")
}
func (preparedStopRoutes) ReadLoaded(context.Context) ([]byte, error) {
	return []byte(`{"apps":{"http":{"servers":{}}}}`), nil
}

func preparedStopGuardFixture(t *testing.T) (nativeBackupRuntime, store.EnginePermit, store.NativeStopObservation, *caddy.Manager, caddy.State, *preparedStopExecutor, map[string]any) {
	t.Helper()
	container, permit, _ := ownedEngineFixture(t)
	stateRoot := t.TempDir()
	material := engineMaterialPaths(stateRoot, permit.Runtime)
	container.Mounts[1].Source, container.Mounts[2].Source = material.privateKey, material.publicKey
	container.State.Running, container.State.PID, container.State.CgroupPath = engineBool(false), new(int), ""
	finished := time.Now().UTC().Add(-time.Second)
	raw, err := json.Marshal(container)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	stopped, ok := fields["State"].(map[string]any)
	if !ok {
		t.Fatal("missing stopped state")
	}
	stopped["ExitCode"], stopped["OOMKilled"], stopped["FinishedAt"], stopped["StoppedByUser"], stopped["Error"] = 0, false, finished, true, ""
	executor := &preparedStopExecutor{id: container.ID}
	executor.container, err = json.Marshal([]any{fields})
	if err != nil {
		t.Fatal(err)
	}
	session, err := localexec.NewSession(executor, nativeBackupFixtureUID(t), t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "gen-1"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("gen-1", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	routes, err := caddy.NewManager(root, preparedStopRoutes{}, preparedStopRoutes{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routes.Close() })
	state := caddy.State{Generation: 1, Files: map[string]string{}, Sites: map[string]caddy.Site{}}
	expected := store.NativeStopObservation{Mode: "clean_exit", ContainerID: container.ID, FinishedAt: finished, SourceAbsent: true, RestartJobsAbsent: true, IngressClosed: true}
	return nativeBackupRuntime{session: session, stateRoot: stateRoot}, permit, expected, routes, state, executor, fields
}

func TestPreparedStopGuardAcceptsCleanContainerWithoutRunningCgroupPath(t *testing.T) {
	runtime, permit, expected, routes, state, executor, _ := preparedStopGuardFixture(t)
	actual, err := runtime.inspectPreparedStop(t.Context(), "game", permit, expected, routes, state, state)
	if err != nil || actual != expected {
		t.Fatal("known clean original stop with absent actual cgroup refused", err)
	}
	if len(executor.commands) != 5 {
		t.Fatal("stopped unit, container inventory or exact finished-state check skipped", len(executor.commands))
	}
}

func TestPreparedStopGuardRejectsChangedOriginalFacts(t *testing.T) {
	for _, attack := range []string{"ownership", "running", "forced", "finished", "unit", "missing"} {
		t.Run(attack, func(t *testing.T) { testPreparedStopGuardAttack(t, attack) })
	}
}

func testPreparedStopGuardAttack(t *testing.T, attack string) {
	t.Helper()
	runtime, permit, expected, routes, state, executor, fields := preparedStopGuardFixture(t)
	stopped, ok := fields["State"].(map[string]any)
	if !ok {
		t.Fatal("missing stopped state")
	}
	switch attack {
	case "ownership":
		fields["Image"] = "foreign"
	case "running":
		stopped["Running"], stopped["Pid"] = true, 7
	case "forced":
		stopped["ExitCode"] = 137
	case "finished":
		expected.FinishedAt = expected.FinishedAt.Add(-time.Second)
	case "unit":
		executor.active = true
	case "missing":
		executor.id = ""
	}
	var err error
	executor.container, err = json.Marshal([]any{fields})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.inspectPreparedStop(t.Context(), "game", permit, expected, routes, state, state); err == nil {
		t.Fatal("changed original stop facts admitted")
	}
}
