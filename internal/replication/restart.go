package replication

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

var ErrRestartUnknown = errors.New("replica restart requires reconciliation")

// RestartHost performs only replica effects. Callers hold the host mutation lock.
type RestartHost interface {
	Inspect(context.Context, data.ReplicaBindingID) (data.ReplicaBinding, bool, error)
	Stop(context.Context, data.ReplicaBinding) error
	Stopped(context.Context, data.ReplicaBinding) (bool, error)
	Acquire(context.Context, data.ReplicaBinding) (func() error, error)
	Commit(context.Context, data.ReplicaBinding, data.ReplicaBinding) error
	Reload(context.Context) error
	Permit(context.Context, data.ReplicaBinding) error
	Start(context.Context, data.ReplicaBinding) error
	Running(context.Context, data.ReplicaBinding) (bool, error)
}

// Restart resumes a durable replica-only cursor. A stop/start intent is committed
// before its effect; unknown attempts are independently inspected, never repeated.
// The stable lifetime lock protects service replacement and binding commitment.
func Restart(ctx context.Context, host RestartHost, before, after data.ReplicaBinding, stage data.RotationStage, advance func(data.RotationStage) error, admitStart func() error) error {
	if host == nil || advance == nil {
		return ErrRestartUnknown
	}
	move := func(next data.RotationStage) error {
		if err := advance(next); err != nil {
			return err
		}
		stage = next
		return nil
	}
	if stage == data.RotationPrepared {
		if err := move(data.RotationStopIssued); err != nil {
			return err
		}
		// Even a failed Stop may have taken effect. Independently inspect below.
		_ = host.Stop(ctx, before)
	}
	if stage == data.RotationStopIssued {
		stopped, err := host.Stopped(ctx, before)
		if err != nil || !stopped {
			return ErrRestartUnknown
		}
		if err := move(data.RotationStopped); err != nil {
			return err
		}
	}
	if stage == data.RotationStopped {
		release, err := host.Acquire(ctx, before)
		if err != nil {
			return ErrRestartUnknown
		}
		commitErr := func() error {
			observed, fenced, err := host.Inspect(ctx, before.BindingID)
			if err != nil || fenced || observed != before && observed != after {
				return ErrRestartUnknown
			}
			if err := host.Commit(ctx, before, after); err != nil {
				return ErrRestartUnknown
			}
			return move(data.RotationCommitted)
		}()
		releaseErr := release()
		if commitErr != nil || releaseErr != nil {
			return ErrRestartUnknown
		}
	}
	if stage == data.RotationCommitted {
		observed, fenced, err := host.Inspect(ctx, after.BindingID)
		if err != nil || fenced || observed != after {
			return ErrRestartUnknown
		}
		if host.Reload(ctx) != nil || host.Permit(ctx, after) != nil {
			return ErrRestartUnknown
		}
		if admitStart != nil {
			if err := admitStart(); err != nil {
				return err
			}
		}
		if err := move(data.RotationStartIssued); err != nil {
			return err
		}
		// Do not compensate or repeat an uncertain start. Running is independent.
		if admitStart != nil {
			if err := admitStart(); err != nil {
				return err
			}
		}
		_ = host.Start(ctx, after)
	}
	if stage != data.RotationStartIssued && stage != data.RotationActive && stage != data.RotationVerified {
		return ErrRestartUnknown
	}
	running, err := host.Running(ctx, after)
	if err != nil || !running {
		return ErrRestartUnknown
	}
	if stage == data.RotationStartIssued {
		if err := move(data.RotationActive); err != nil {
			return err
		}
	}
	return nil
}
