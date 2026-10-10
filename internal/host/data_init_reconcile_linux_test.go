//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/systemd"
	"testing"
)

type initializationDeadRunner struct{ unknown bool }

func (r initializationDeadRunner) Show(context.Context, systemd.Unit) (systemd.Properties, error) {
	if r.unknown {
		return systemd.Properties{}, errors.New("unknown manager")
	}
	return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
}
func (r initializationDeadRunner) JobPending(context.Context, systemd.Unit) (bool, error) {
	return false, nil
}

func TestInitializationStandaloneReconcileReleasesUntouchedFenceWithoutReplay(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "unknown"}[unknown], func(t *testing.T) {
			ctx := context.Background()
			state, engine, p := initializationJobFixture(t, datainit.LocalOperatorRequester())
			uploads := 0
			engine.PrepareRestorePoint = func(context.Context, datainit.Plan, datainit.Operation) (datainit.VerifiedRestorePoint, error) {
				uploads++
				return datainit.VerifiedRestorePoint{}, errors.New("unexpected upload")
			}
			engine.Quiesce = func(context.Context, datainit.Plan) (func(), error) {
				return nil, errors.New("interrupted after claim")
			}
			interrupted, err := engine.Apply(ctx, p.Request.App, p.ID)
			if !errors.Is(err, datainit.ErrRecovery) || interrupted.State != "intent" {
				t.Fatal("did not interrupt after claim", err)
			}
			job, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.DataInitApply, App: p.Request.App, SecretRef: p.ID}, p.Requester.String(), "init-recovery")
			if err != nil {
				t.Fatal(err)
			}
			if err = state.TransitionOperation(ctx, job.ID, ops.Queued, ops.Preflight); err != nil {
				t.Fatal(err)
			}
			// Simulate a dead process after the durable fence claim, before a task outcome.

			r := initializationReconciler{Reconciler: reconcile.Reconciler{Store: state, Systemd: initializationDeadRunner{unknown: unknown}}, service: Service{Store: state}, engine: func(context.Context, ops.Operation) (datainit.Service, error) { return engine, nil }}
			report, err := r.Reconcile(ctx)
			if err != nil || len(report.Outcomes) != 1 {
				t.Fatal("standalone reconcile failed", err, report)
			}
			retained, _, err := state.ReadInitOperation(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := state.ReadReplicaPermit(ctx, p.Database.DatabaseID)
			if err != nil {
				t.Fatal(err)
			}
			if unknown {
				if retained.State != "intent" || permit.FenceState != "held" {
					t.Fatal("unknown runner released fence")
				}
			} else {
				if retained.State != "not_initialized" || permit.FenceState == "held" || report.Outcomes[0].After != ops.Failed {
					t.Fatal("untouched initialization not settled", retained, permit.FenceState)
				}
				// A second reconciliation neither replays the task nor changes its immutable outcome.
				again, err := r.Reconcile(ctx)
				if err != nil || len(again.Outcomes) != 0 {
					t.Fatal("settlement replayed", err)
				}
			}
			facts, err := engine.Facts(ctx, p.Request)
			if err != nil || facts.Observation.State != "allocated_empty" || uploads != 0 {
				t.Fatal("recovery replayed upload or live mutation", err, uploads)
			}
		})
	}
}
