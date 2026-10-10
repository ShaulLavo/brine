//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/systemd"
	"time"
)

// initializationReconciler extends boot/standalone reconciliation with bounded
// inspect-only settlement. The original detached outcome stays immutable.
type initializationReconciler struct {
	reconcile.Reconciler
	service   Service
	stateRoot string
	engine    func(context.Context, ops.Operation) (datainit.Service, error)
}

func (r initializationReconciler) Reconcile(ctx context.Context) (reconcile.Report, error) {
	report, err := r.Reconciler.Reconcile(ctx)
	if err != nil {
		return report, err
	}
	recovered, err := r.settleInitialization(ctx)
	for _, settled := range recovered {
		found := false
		for index, old := range report.Outcomes {
			if old.OperationID == settled.OperationID {
				settled.Before = old.Before
				report.Outcomes[index] = settled
				found = true
				break
			}
		}
		if !found {
			report.Outcomes = append(report.Outcomes, settled)
		}
	}
	return report, err
}
func (r initializationReconciler) settleInitialization(ctx context.Context) (out []reconcile.Outcome, err error) {
	lock, err := r.service.Store.TryAcquireHostLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	pending, err := r.service.Store.InitializationRecoveryJobs(ctx)
	if err != nil {
		return nil, err
	}
	for _, job := range pending {
		outcome := reconcile.Outcome{OperationID: job.ID, Before: job.State, After: job.State, Action: "recovery_required", Code: "recovery_required"}
		unit, err := systemd.ParseUnit("brine-op-" + job.ID + ".service")
		if err != nil {
			return nil, err
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		properties, showErr := r.Systemd.Show(probe, unit)
		queued, queueErr := r.Systemd.JobPending(probe, unit)
		cancel()
		var missing *localexec.Error
		absent := errors.As(showErr, &missing) && missing.Kind == localexec.NotFound
		dead := queueErr == nil && !queued && (absent || showErr == nil && (properties.ActiveState == "inactive" && properties.SubState == "dead" || properties.ActiveState == "failed" && properties.SubState == "failed" || properties.ActiveState == "active" && properties.SubState == "exited"))
		if !dead {
			out = append(out, outcome)
			continue
		}
		factory := r.engine
		if factory == nil {
			factory = func(ctx context.Context, job ops.Operation) (datainit.Service, error) {
				return initializationTaskEngine(ctx, r.service, r.stateRoot, job)
			}
		}
		engine, loadErr := factory(ctx, job)
		if loadErr == nil {
			// This composition already holds the host mutation lock. No second lock or
			// writer/upload adapter is invoked by the inspect-only domain operation.
			engine.Lock = func(context.Context) (func(), error) { return func() {}, nil }
			initialized, inspectErr := engine.Reconcile(ctx, job.App, job.SecretRef)
			if inspectErr == nil {
				outcome.Code = ""
				outcome.After = ops.Succeeded
				outcome.Action = "succeeded"
				if initialized.State == "not_initialized" {
					outcome.After = ops.Failed
					outcome.Action = "failed"
				}
			}
		}
		out = append(out, outcome)
	}
	return out, nil
}

func (r initializationReconciler) recoveryJob() func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		r.Reconciler.ExcludeID = id
		r.Reconciler.LockTimeout = jobs.HostLockWaitTimeout
		return recoveryResult(r.Reconciler.Reconcile(ctx))
	}
}
