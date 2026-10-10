package host

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

// Recovery shares the normal executor's operational adapters. Each assessment
// binds its facts reader to that operation's stored desired input, not a latest
// plan or a previous operation's cached inventory.
func newReconciler(service Service, engine apply.Executor, inspector reconcile.RunnerInspector) reconcile.Reconciler {
	return reconcile.Reconciler{SecretResolution: func(ctx context.Context, op ops.Operation, events []ops.Event) (ops.State, error) {
		if service.Policy == nil {
			return ops.RecoveryRequired, result.New(result.DependencyMissing, nil)
		}
		pol, err := service.Policy.Load(ctx)
		if err != nil {
			return ops.RecoveryRequired, err
		}
		if err = pol.CheckSecret(spec.Name(op.App), spec.SecretReference(op.SecretRef)); err != nil {
			return ops.RecoveryRequired, err
		}
		name := ""
		for _, event := range events {
			if event.Kind != "secret_version" {
				continue
			}
			var payload ops.SecretVersionPayload
			if json.Unmarshal(event.Payload, &payload) != nil || !strings.HasPrefix(payload.Name, "brine."+op.App+"."+op.SecretRef+".v") || name != "" && name != payload.Name {
				return ops.RecoveryRequired, nil
			}
			name = payload.Name
		}
		if name == "" || engine.Podman == nil {
			return ops.RecoveryRequired, nil
		}
		parsed, err := podman.ParseSecretName(name)
		if err != nil {
			return ops.RecoveryRequired, err
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		exists, err := engine.Podman.SecretExists(probe, parsed)
		if err != nil {
			return ops.RecoveryRequired, err
		}
		if exists {
			return ops.Succeeded, nil
		}
		return ops.Failed, nil
	}, Store: service.Store, Systemd: inspector, ExecutorFor: func(_ context.Context, _ ops.Operation, p plan.Plan, d policy.Desired) (*apply.Executor, error) {
		copy := engine
		copy.Facts = operationFacts{service: service, desired: d, removal: p.Lifecycle == plan.RemoveApp}
		return &copy, nil
	}}
}

type operationFacts struct {
	service Service
	desired policy.Desired
	removal bool
}

func (f operationFacts) Read(ctx context.Context) (apply.Facts, error) {
	if f.removal {
		return f.service.removalFacts(ctx, f.desired)
	}
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
		return recoveryResult(copy.Reconcile(ctx))
	}
}

func (r runnerReconciler) RunResolution(ctx context.Context, lock ops.Lock, id string) error {
	return r.reconciler.RunResolution(ctx, lock, id)
}

func recoveryResult(report reconcile.Report, err error) error {
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
