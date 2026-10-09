//go:build linux

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/cli"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
)

// This file-backed fixture is not a store implementation. The parent writes
// before exit and the job writes only after the parent has exited.
type disconnectStore struct{ dir string }
type fixtureRecord struct {
	Operation ops.Operation `json:"operation"`
	Events    []ops.Event   `json:"events"`
}

func (s disconnectStore) read() (fixtureRecord, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, "record.json"))
	var record fixtureRecord
	if err == nil {
		err = json.Unmarshal(raw, &record)
	}
	return record, err
}
func (s disconnectStore) write(record fixtureRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "record.json"), raw, 0600)
}
func (s disconnectStore) CreateOperation(_ context.Context, planID, requester, key string) (ops.Operation, bool, error) {
	record, err := s.read()
	if err == nil {
		return record.Operation, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ops.Operation{}, false, err
	}
	op := ops.Operation{ID: "op1", PlanID: planID, Requester: requester, IdempotencyKey: key, State: ops.Queued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	return op, false, s.write(fixtureRecord{Operation: op, Events: []ops.Event{}})
}
func (s disconnectStore) GetOperation(context.Context, string) (ops.Operation, error) {
	record, err := s.read()
	return record.Operation, err
}
func (s disconnectStore) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return plan.Plan{Hash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, policy.Desired{}, nil
}
func (s disconnectStore) AcquireHostLock(context.Context) (jobs.Lock, error) {
	return fixtureLock{}, nil
}

type fixtureLock struct{}

func (fixtureLock) Release() error { return nil }
func (s disconnectStore) AppendEvent(_ context.Context, _ string, event ops.Event) (uint64, error) {
	if event.Kind == "state" {
		return 0, ops.ErrInvalidEvent
	}
	record, err := s.read()
	if err != nil {
		return 0, err
	}
	if err = ops.ValidateEvent(event); err != nil {
		return 0, err
	}
	event.Sequence = uint64(len(record.Events) + 1)
	event.CreatedAt = time.Now().UTC()
	record.Events = append(record.Events, event)
	return event.Sequence, s.write(record)
}
func (s disconnectStore) TransitionOperation(ctx context.Context, id string, from, to ops.State) error {
	record, err := s.read()
	if err != nil {
		return err
	}
	if record.Operation.State != from {
		return &ops.StateConflictError{Current: record.Operation.State}
	}
	return s.SetOperationState(ctx, id, to)
}
func (s disconnectStore) SetOperationState(_ context.Context, _ string, state ops.State) error {
	record, err := s.read()
	if err != nil {
		return err
	}
	record.Operation.State = state
	record.Operation.UpdatedAt = time.Now().UTC()
	record.Events = append(record.Events, ops.Event{Sequence: uint64(len(record.Events) + 1), Kind: "state", State: state, CreatedAt: record.Operation.UpdatedAt})
	return s.write(record)
}
func (s disconnectStore) EventsAfter(_ context.Context, _ string, cursor uint64, limit int) ([]ops.Event, error) {
	record, err := s.read()
	out := []ops.Event{}
	for _, event := range record.Events {
		if event.Sequence > cursor && len(out) < limit {
			out = append(out, event)
		}
	}
	return out, err
}

type fixtureExecutor struct{ store disconnectStore }

func (e fixtureExecutor) Run(ctx context.Context, id string, _ plan.Plan, _ policy.Desired) error {
	return e.store.SetOperationState(ctx, id, ops.Succeeded)
}

type fakeSystemdRun struct{ dir, binary string }

func (f fakeSystemdRun) Execute(ctx context.Context, command localexec.Command) (localexec.Result, error) {
	if command.Path != "systemd-run" {
		return localexec.Result{}, errors.New("unexpected command")
	}
	// Execute a separate fake systemd-run process, not a goroutine in the SSH parent.
	child := exec.CommandContext(ctx, f.binary, "-test.run=^TestDetachedProcessHelper$")
	child.Env = append(os.Environ(), "BRINE_JOB_HELPER=systemd-run", "BRINE_JOB_DIR="+f.dir, "BRINE_JOB_PARENT="+strconv.Itoa(os.Getpid()))
	raw, err := json.Marshal(command.Args)
	if err != nil {
		return localexec.Result{}, err
	}
	child.Stdin = bytes.NewReader(raw)
	return localexec.Result{}, child.Run()
}

func TestDetachedProcessHelper(t *testing.T) {
	role := os.Getenv("BRINE_JOB_HELPER")
	if role == "" {
		return
	}
	dir := os.Getenv("BRINE_JOB_DIR")
	store := disconnectStore{dir: dir}
	binary, err := os.Executable()
	if err != nil {
		os.Exit(10)
	}
	switch role {
	case "ssh-parent":
		session, err := localexec.NewSession(fakeSystemdRun{dir: dir, binary: binary}, 1234, systemd.JobWorkingDirectory, time.Second*5)
		if err != nil {
			os.Exit(11)
		}
		service := jobs.Service{Store: store, Requester: "fixture", Launcher: systemd.NewJobLauncher(session, 1234)}
		deps := cli.Dependencies{Context: context.Background(), Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Version: "fixture", HostUID: func() int { return 1234 }, HostJobs: service, HostAuthorization: func(context.Context, dispatch.Class) error { return nil }}
		os.Exit(result.ExitCode(cli.Execute(deps, []string{"host", "serve"})))
	case "systemd-run":
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(12)
		}
		var args []string
		if json.Unmarshal(raw, &args) != nil || len(args) < 5 {
			os.Exit(13)
		}
		tail := args[len(args)-5:]
		if strings.Join(tail, " ") != "-- /usr/local/bin/brine host run-op op1" {
			os.Exit(14)
		}
		if err = os.WriteFile(filepath.Join(dir, "unit"), []byte("loaded"), 0600); err != nil {
			os.Exit(15)
		}
		job := exec.Command(binary, "-test.run=^TestDetachedProcessHelper$")
		job.Env = append(os.Environ(), "BRINE_JOB_HELPER=unit-job")
		job.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err = job.Start(); err != nil {
			os.Exit(16)
		}
		_ = job.Process.Release()
		os.Exit(0)
	case "unit-job":
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(dir, "parent-exited")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(17)
			}
			time.Sleep(10 * time.Millisecond)
		}
		pid, _ := strconv.Atoi(os.Getenv("BRINE_JOB_PARENT"))
		if syscall.Kill(pid, 0) == nil {
			os.Exit(18)
		}
		runner := jobs.Runner{Store: store, Executor: fixtureExecutor{store: store}}
		deps := cli.Dependencies{Context: context.Background(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, Version: "fixture", HostUID: func() int { return 1234 }, HostOperationRunner: runner}
		if err = cli.Execute(deps, []string{"host", "run-op", "op1"}); err != nil {
			os.Exit(19)
		}
		if err = os.Remove(filepath.Join(dir, "unit")); err != nil {
			os.Exit(20)
		}
		if err = os.WriteFile(filepath.Join(dir, "finished"), []byte("collected"), 0600); err != nil {
			os.Exit(21)
		}
		os.Exit(0)
	default:
		os.Exit(22)
	}
}

func TestDispatcherJobSurvivesSSHParentExitAndCollection(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, binary, "-test.run=^TestDetachedProcessHelper$")
	parent.Env = append(os.Environ(), "BRINE_JOB_HELPER=ssh-parent", "BRINE_JOB_DIR="+dir)
	parent.Stdin = strings.NewReader(`{"schema_version":1,"op":"apply","request_id":"disconnect-fixture","args":{"plan_id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","idempotency_key":"fixture-key"}}`)
	var stdout, stderr bytes.Buffer
	parent.Stdout = &stdout
	parent.Stderr = &stderr
	if err := parent.Run(); err != nil {
		t.Fatalf("parent: %v stderr=%s", err, stderr.String())
	}
	response, err := dispatch.DecodeResponse(stdout.Bytes(), "apply")
	if err != nil {
		t.Fatalf("response=%s err=%v", stdout.String(), err)
	}
	accepted, ok := response.Data.(jobs.Accepted)
	if !ok || accepted.OperationID != "op1" || accepted.Status != "accepted" {
		t.Fatalf("not accepted: %+v", response)
	}
	if err = os.WriteFile(filepath.Join(dir, "parent-exited"), []byte("exited"), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err = os.Stat(filepath.Join(dir, "finished")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("job did not survive parent", ctx.Err())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "unit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit not collected: %v", err)
	}
	service := jobs.Service{Store: disconnectStore{dir: dir}}
	input := `{"schema_version":1,"op":"operation","request_id":"poll","args":{"operation_id":"op1","after_cursor":0}}`
	response, err = dispatch.NewServer("fixture", nil).WithJobs(service, nil).Handle(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	status, ok := response.Data.(jobs.Status)
	if !ok || status.Operation.State != ops.Succeeded || len(status.Events) != 3 {
		t.Fatalf("not completed after collection: %+v", response)
	}
}
