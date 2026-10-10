//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type restoreAdmission struct{ stateDir string }

func (a restoreAdmission) Test(ctx context.Context, input dispatch.RestoreTestArgs) (jobs.Accepted, error) {
	if !input.Valid() {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	service, state, err := openMetadataJobs(ctx, a.stateDir, true)
	if err != nil {
		return jobs.Accepted{}, err
	}
	defer func() { _ = state.Close() }()
	reference, err := state.SaveRestoreTaskInput(ctx, service.Requester, input)
	if err != nil {
		return jobs.Accepted{}, err
	}
	key, err := data.NewID()
	if err != nil {
		return jobs.Accepted{}, err
	}
	return service.Submit(ctx, ops.Intent{Kind: ops.RestoreTest, App: input.App, SecretRef: reference}, key)
}

// This composition opens only protected control metadata and the job launcher.
// It never opens mutation runtime, live inventory, policy or a host mutation lock.
func openMetadataJobs(ctx context.Context, dir string, admission bool) (jobs.Service, *store.Store, error) {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil || identity.Username != "brine" || identity.HomeDir != "/home/brine" || os.Geteuid() == 0 {
		return jobs.Service{}, nil, result.New(result.DispatchOperationRefused, nil)
	}
	uid, err := strconv.ParseUint(identity.Uid, 10, 32)
	if err != nil {
		return jobs.Service{}, nil, err
	}
	requester := ""
	if admission {
		raw, err := trustedRead(ctx, RequesterPath, 256)
		if err != nil {
			return jobs.Service{}, nil, err
		}
		requester = strings.TrimSpace(string(raw))
		if !regexpRequester(requester) {
			return jobs.Service{}, nil, result.New(result.DispatchOperationRefused, nil)
		}
	}
	session, err := localexec.NewSession(localexec.ExecRunner{}, uint32(uid), identity.HomeDir, systemd.LaunchProbeTimeout)
	if err != nil {
		return jobs.Service{}, nil, err
	}
	state, err := store.OpenContext(ctx, dir)
	if err != nil {
		return jobs.Service{}, nil, err
	}
	return jobs.Service{Store: state, Launcher: systemd.NewJobLauncher(session, uint32(uid)), Requester: requester}, state, nil
}

func restoreTaskHandler(state *store.Store, dir string) jobs.TaskHandler {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		input, err := state.LoadRestoreTaskInput(ctx, op.SecretRef, op.Requester)
		if err != nil || input.App != op.App {
			return nil, result.New(result.PolicyRefused, nil)
		}
		receipt, err := (restoreTests{stateDir: dir}).Check(ctx, input, op.ID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
}

// DetachedRunner chooses restore's metadata-only worker before opening the
// mutation composition. Other operation kinds retain the normal runtime.
type DetachedRunner struct{}

func (DetachedRunner) Run(ctx context.Context, id string) error {
	dir := "/home/brine/.local/state/brine"
	state, err := store.OpenReadOnly(ctx, dir)
	if err != nil {
		return err
	}
	operation, readErr := state.GetOperation(ctx, id)
	_ = state.Close()
	if readErr != nil {
		return readErr
	}
	if operation.Kind != ops.RestoreTest {
		runtime, err := Open(ctx, "")
		if err != nil {
			return err
		}
		defer func() { _ = runtime.Close() }()
		return runtime.Runner.Run(ctx, id)
	}
	_, state, err = openMetadataJobs(ctx, dir, false)
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	runner := jobs.Runner{Store: state, TaskHandlers: map[ops.Kind]jobs.TaskHandler{ops.RestoreTest: restoreTaskHandler(state, dir)}}
	return runner.Run(ctx, id)
}
