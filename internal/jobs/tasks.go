package jobs

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

// TaskHandler reconstructs its input from an immutable reference and returns a
// closed receipt. It owns any required host locking; read-only tasks need none.
type TaskHandler func(context.Context, ops.Operation) (json.RawMessage, error)

// Submit journals reference-only identity atomically before launching detached
// work. It does not collect live inventory or acquire the host mutation lock.
func (s Service) Submit(ctx context.Context, intent ops.Intent, key string) (accepted Accepted, err error) {
	if s.Store == nil || s.Launcher == nil || s.Requester == "" {
		return Accepted{}, result.New(result.DependencyMissing, nil)
	}
	if !intent.Kind.IsTask() || !ops.ValidIntent(intent) || !ValidID(key) {
		return Accepted{}, result.New(result.DispatchInvalidRequest, nil)
	}
	wait, stop := context.WithTimeout(ctx, LaunchLockWaitTimeout)
	lock, err := s.Store.AcquireLaunchLock(wait)
	stop()
	if err != nil {
		return Accepted{}, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	operation, existing, err := s.Store.CreateOperation(ctx, intent, s.Requester, key)
	if err != nil {
		return Accepted{}, err
	}
	return s.launch(ctx, operation, existing)
}

func (r Runner) runTask(ctx context.Context, op ops.Operation) error {
	handler := r.TaskHandlers[op.Kind]
	state, ok := r.Store.(interface {
		CompleteTask(context.Context, string, ops.State, ops.TaskOutcome) error
	})
	if handler == nil || !ok {
		return result.New(result.DependencyMissing, nil)
	}
	if op.State != ops.Queued && op.State != ops.LaunchUnknown {
		return result.New(result.Conflict, nil)
	}
	// An independently running task wins this CAS before invoking any effects.
	if err := r.Store.TransitionOperation(ctx, op.ID, op.State, ops.Preflight); err != nil {
		return err
	}
	receipt, runErr := handler(ctx, op)
	outcome := ops.TaskOutcome{Receipt: receipt}
	to := ops.Succeeded
	terminal := op
	terminal.State = to
	if runErr == nil && !outcome.Valid(terminal) {
		runErr = result.New(result.InternalError, nil)
	}
	if runErr != nil {
		to = ops.RecoveryRequired
		if op.Kind == ops.RestoreTest {
			to = ops.Failed
		}
		outcome = ops.TaskOutcome{Error: result.Classify(runErr).Code()}
	}
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	completeErr := state.CompleteTask(journal, op.ID, to, outcome)
	return errors.Join(runErr, completeErr)
}
