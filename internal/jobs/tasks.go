package jobs

import (
	"bytes"
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
	if isSQLiteRetireTask(op.Kind) {
		outcome, err := r.readSQLiteRetireResult(ctx, op)
		if err != nil {
			return err
		}
		if outcome != nil {
			return settledSQLiteTask(op, outcome, nil, nil)
		}
	}
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
	op.State = ops.Preflight
	receipt, runErr := handler(ctx, op)
	if isSQLiteRetireTask(op.Kind) {
		return r.finishSQLiteRetireTask(ctx, op, receipt, runErr)
	}
	if op.Kind == ops.NativePrepareAbandon {
		return r.finishNativeAbandonTask(ctx, op, receipt, runErr, state)
	}
	if isSQLitePreparationTask(op.Kind) {
		return r.finishSQLiteTask(ctx, op, receipt, runErr)
	}
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

func (r Runner) finishNativeAbandonTask(ctx context.Context, original ops.Operation, receipt json.RawMessage, runErr error, completer interface {
	CompleteTask(context.Context, string, ops.State, ops.TaskOutcome) error
}) error {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	reader, ok := r.Store.(interface {
		ReadNativeAbandonmentOutcome(context.Context, string) (*ops.TaskOutcome, error)
	})
	if !ok {
		return result.New(result.DependencyMissing, nil)
	}
	outcome, err := reader.ReadNativeAbandonmentOutcome(journal, original.ID)
	if err != nil {
		return errors.Join(runErr, err)
	}
	current, err := r.Store.GetOperation(journal, original.ID)
	if err != nil {
		return errors.Join(runErr, err)
	}
	if current.Kind != original.Kind || current.Requester != original.Requester || current.App != original.App || current.SecretRef != original.SecretRef || current.IdempotencyKey != original.IdempotencyKey {
		return result.New(result.Conflict, nil)
	}
	if current.State == ops.Succeeded {
		if outcome == nil || !outcome.Valid(current) || len(receipt) != 0 && !bytes.Equal(receipt, outcome.Receipt) {
			return result.New(result.InternalError, nil)
		}
		return runErr
	}
	if current.State != ops.Preflight || outcome != nil {
		return errors.Join(runErr, result.New(result.Conflict, nil))
	}
	if runErr == nil {
		runErr = result.New(result.InternalError, nil)
	}
	return errors.Join(runErr, completer.CompleteTask(journal, original.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.Classify(runErr).Code()}))
}

type sqliteRetireStore interface {
	ReadSQLiteRetireOutcome(context.Context, string) (*ops.TaskOutcome, error)
	FailUnclaimedSQLiteRetire(context.Context, ops.Operation, string) error
	MarkSQLiteRetireRecovery(context.Context, ops.Operation, string) error
}

func isSQLiteRetireTask(kind ops.Kind) bool {
	return kind == ops.SQLiteRetire || kind == ops.SQLiteRetireResolve
}

func (r Runner) readSQLiteRetireResult(ctx context.Context, op ops.Operation) (*ops.TaskOutcome, error) {
	store, ok := r.Store.(sqliteRetireStore)
	if !ok {
		return nil, result.New(result.DependencyMissing, nil)
	}
	return store.ReadSQLiteRetireOutcome(ctx, op.ID)
}

func (r Runner) finishSQLiteRetireTask(ctx context.Context, original ops.Operation, receipt json.RawMessage, runErr error) error {
	journal, cancel := context.WithTimeout(context.WithoutCancel(ctx), JournalTimeout)
	defer cancel()
	store, ok := r.Store.(sqliteRetireStore)
	if !ok {
		return result.New(result.DependencyMissing, nil)
	}
	outcome, err := store.ReadSQLiteRetireOutcome(journal, original.ID)
	if err != nil {
		return errors.Join(runErr, err)
	}
	current, err := r.Store.GetOperation(journal, original.ID)
	if err != nil {
		return errors.Join(runErr, err)
	}
	if current.Kind != original.Kind || current.Requester != original.Requester || current.App != original.App || current.SecretRef != original.SecretRef || current.RecoveryOf != original.RecoveryOf || current.IdempotencyKey != original.IdempotencyKey {
		return result.New(result.Conflict, nil)
	}
	if current.State.IsTerminal() {
		return settledSQLiteTask(current, outcome, receipt, runErr)
	}
	if current.State != ops.Preflight || outcome != nil {
		return errors.Join(runErr, result.New(result.Conflict, nil))
	}
	if runErr == nil {
		runErr = result.New(result.InternalError, nil)
	}
	if err = store.FailUnclaimedSQLiteRetire(journal, current, string(result.Classify(runErr).Code())); err == nil {
		return runErr
	}
	return errors.Join(runErr, store.MarkSQLiteRetireRecovery(journal, current, string(result.Classify(runErr).Code())))
}
