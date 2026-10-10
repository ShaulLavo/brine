package apply

import (
	"context"

	"github.com/ShaulLavo/brine/internal/replication"
)

type ReplicaActivation struct {
	replication.ReplicaPermitRequest
	LifetimeLock string
}
type ReplicaServices interface {
	Start(context.Context, string) error
	Stop(context.Context, string) error
	// ReplicaStopped independently checks manager jobs, service PID and process
	// state. An unknown process or pending manager job must return false/error.
	ReplicaStopped(context.Context, string) (bool, error)
}
type ReplicaLock interface{ Release() error }
type ReplicaLocks interface {
	Acquire(context.Context, string) (ReplicaLock, error)
}
type KernelReplicaLocks struct{}

func (KernelReplicaLocks) Acquire(ctx context.Context, file string) (ReplicaLock, error) {
	return replication.AcquireLifetimeLock(ctx, file)
}

// ReplicaOrchestrator runs under the caller's host mutation lock. App replacement
// does not call Quiesce. Only separately journaled fenced operations stop replicas.
type ReplicaOrchestrator struct {
	Permits  replication.PermitReader
	Services ReplicaServices
	Locks    ReplicaLocks
}

func (r ReplicaOrchestrator) Activate(ctx context.Context, b ReplicaActivation) error {
	if r.Services == nil || replication.ReplicaPermit(ctx, r.Permits, b.ReplicaPermitRequest) != nil {
		return &Error{Step: "start_replica", Code: "replica_permit_refused", State: Failed}
	}
	name, err := replication.ServiceName(b.BindingID)
	if err != nil {
		return err
	}
	if err = r.Services.Start(ctx, name); err != nil {
		return &Error{Step: "start_replica", Code: "replica_activation_unknown", State: RecoveryRequired, Cause: err}
	}
	// Starting a process creates no remote watermark or backup-ready receipt.
	return nil
}
func (r ReplicaOrchestrator) Quiesce(ctx context.Context, b ReplicaActivation) error {
	if r.Permits == nil || r.Services == nil || r.Locks == nil {
		return &Error{Step: "stop_replica", Code: "replica_fence_required", State: RecoveryRequired}
	}
	state, err := r.Permits.ReadReplicaPermit(ctx, b.BindingID)
	if err != nil || ctx.Err() != nil || state.Fence != replication.FenceHeld || state.Binding.BindingID != b.BindingID || state.LifetimeLock != b.LifetimeLock {
		return &Error{Step: "stop_replica", Code: "replica_fence_required", State: RecoveryRequired, Cause: err}
	}
	name, err := replication.ServiceName(b.BindingID)
	if err != nil {
		return err
	}
	if err = r.Services.Stop(ctx, name); err != nil {
		return &Error{Step: "stop_replica", Code: "replica_stop_unknown", State: RecoveryRequired, Cause: err}
	}
	stopped, err := r.Services.ReplicaStopped(ctx, name)
	if err != nil || !stopped {
		return &Error{Step: "stop_replica", Code: "replica_stop_unknown", State: RecoveryRequired, Cause: err}
	}
	lock, err := r.Locks.Acquire(ctx, b.LifetimeLock)
	if err != nil {
		return &Error{Step: "stop_replica", Code: "replica_lock_held", State: RecoveryRequired, Cause: err}
	}
	if err = lock.Release(); err != nil {
		return &Error{Step: "stop_replica", Code: "replica_lock_unknown", State: RecoveryRequired, Cause: err}
	}
	return nil
}
