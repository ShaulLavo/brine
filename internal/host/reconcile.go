package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
)

// Recovery shares the normal executor's operational adapters. Each assessment
// binds its facts reader to that operation's stored desired input, not a latest
// plan or a previous operation's cached inventory.
func newReconciler(service Service, engine apply.Executor, inspector reconcile.RunnerInspector) reconcile.Reconciler {
	return reconcile.Reconciler{Store: service.Store, Systemd: inspector, ExecutorFor: func(_ context.Context, _ ops.Operation, _ plan.Plan, d policy.Desired) (*apply.Executor, error) {
		copy := engine
		copy.Facts = operationFacts{service: service, desired: d}
		return &copy, nil
	}}
}

type operationFacts struct {
	service Service
	desired policy.Desired
}

func (f operationFacts) Read(ctx context.Context) (apply.Facts, error) {
	return f.service.facts(ctx, App(f.desired))
}

// The runner hook intentionally has an error-only contract: run-op owns the
// host lock and needs successful settlement, not a second presentation.
type runnerReconciler struct{ reconciler reconcile.Reconciler }

func (r runnerReconciler) ReconcileUnderLock(ctx context.Context, lock ops.Lock, id string, dry bool) error {
	_, err := r.reconciler.ReconcileUnderLock(ctx, lock, id, dry)
	return err
}

func recoveryJob(reconciler reconcile.Reconciler) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		copy := reconciler
		copy.ExcludeID = id
		// Detached jobs may wait for an ongoing deploy, unlike synchronous previews.
		copy.LockTimeout = jobs.HostLockWaitTimeout
		report, err := copy.Reconcile(ctx)
		if err != nil {
			return err
		}
		for _, out := range report.Outcomes {
			if out.After == ops.RecoveryRequired {
				return result.New(result.RecoveryRequired, nil)
			}
		}
		return nil
	}
}
