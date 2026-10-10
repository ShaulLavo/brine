//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/systemd"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A journal boundary panic leaves the durable cursor exactly as a killed runner does.
type interruptedInitializationJournal struct {
	datainit.Journal
	boundary string
}

func (j interruptedInitializationJournal) ClaimInitialization(ctx context.Context, p datainit.Plan) (datainit.Operation, error) {
	op, err := j.Journal.ClaimInitialization(ctx, p)
	if err == nil && j.boundary == "intent" {
		panic("interrupted initialization")
	}
	return op, err
}
func (j interruptedInitializationJournal) SetInitState(ctx context.Context, op datainit.Operation, state string) (datainit.Operation, error) {
	next, err := j.Journal.SetInitState(ctx, op, state)
	if err == nil && j.boundary == state {
		panic("interrupted initialization")
	}
	return next, err
}
func interruptInitialization(t *testing.T, engine datainit.Service, p datainit.Plan) {
	t.Helper()
	defer func() {
		if recover() != "interrupted initialization" {
			t.Fatal("initialization did not reach interruption")
		}
	}()
	_, _ = engine.Apply(context.Background(), p.Request.App, p.ID)
}
func initializationRecoveryPoint(p datainit.Plan, op datainit.Operation) datainit.VerifiedRestorePoint {
	now := time.Now().UTC()
	snapshot := restore.SnapshotSource{BindingID: string(p.Database.ReplicaBindingID), Epoch: string(p.ReplicaEpoch), PointID: p.RestorePointID, ObjectKey: p.RemotePrefix + "/restore-points/" + p.RestorePointID + "/snapshot.sqlite", SHA256: strings.Repeat("a", 64), Size: 4096}
	return datainit.VerifiedRestorePoint{PointID: p.RestorePointID, DatabaseID: p.Database.DatabaseID, UploadedAt: now, RetainUntil: now.Add(2 * time.Hour), Receipt: restore.Receipt{ToolVersion: restore.SnapshotToolVersion, OperationID: op.ID + "-empty-verify", Source: restore.RestoreSource{Kind: restore.SQLiteSnapshot, Snapshot: &snapshot}, ObservedAt: now, Schema: restore.SchemaObservation{State: restore.VerifiedEmpty, Marker: data.EmptyMarker, CatalogSHA256: data.EmptyCatalogSHA256}, IntegrityCheck: "passed", ForeignKeyCheck: "passed", InvariantCheck: "passed"}}
}
func TestDetachedInitializationRecoveryAtEveryBoundary(t *testing.T) {
	for _, boundary := range []string{"intent", "quiesced", "restore_point_intent", "upload", "restore_point_verified", "mutation_intent", "mutation_completed", "unknown_schema", "missing_schema_receipt"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			state, engine, p := initializationJobFixture(t, datainit.LocalOperatorRequester())
			cursor := boundary
			if boundary == "upload" {
				cursor = "restore_point_intent"
			}
			if boundary == "unknown_schema" || boundary == "missing_schema_receipt" {
				cursor = "mutation_completed"
			}
			engine.Journal = interruptedInitializationJournal{Journal: state, boundary: cursor}
			engine.PrepareRestorePoint = func(_ context.Context, p datainit.Plan, op datainit.Operation) (datainit.VerifiedRestorePoint, error) {
				return initializationRecoveryPoint(p, op), nil
			}
			if boundary == "upload" {
				engine.Journal = interruptedInitializationJournal{Journal: state, boundary: "never"}
				engine.PrepareRestorePoint = func(_ context.Context, p datainit.Plan, op datainit.Operation) (datainit.VerifiedRestorePoint, error) {
					_ = initializationRecoveryPoint(p, op)
					panic("interrupted initialization")
				}
			}
			interruptInitialization(t, engine, p)
			engine.Journal = state
			if boundary == "unknown_schema" {
				if err := os.WriteFile(filepath.Join(string(p.Database.Root), p.Database.RelativeDirectory, string(p.Database.Filename)), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "missing_schema_receipt" {
				engine.Journal = initializationMissingReceipt{Journal: state}
			}
			engine.Quiesce = func(context.Context, datainit.Plan) (func(), error) {
				t.Fatal("recovery quiesced again")
				return nil, nil
			}
			engine.PrepareRestorePoint = func(context.Context, datainit.Plan, datainit.Operation) (datainit.VerifiedRestorePoint, error) {
				t.Fatal("recovery uploaded again")
				return datainit.VerifiedRestorePoint{}, nil
			}
			source, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.DataInitApply, App: p.Request.App, SecretRef: p.ID}, p.Requester.String(), "interrupted-init")
			if err != nil {
				t.Fatal(err)
			}
			if err = state.TransitionOperation(ctx, source.ID, ops.Queued, ops.Preflight); err != nil {
				t.Fatal(err)
			}
			outer, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.Reconcile}, p.Requester.String(), "recover-init")
			if err != nil {
				t.Fatal(err)
			}
			r := initializationReconciler{Reconciler: reconcile.Reconciler{Store: state, Systemd: initializationDeadRunner{}}, service: Service{Store: state}, engine: func(context.Context, ops.Operation) (datainit.Service, error) { return engine, nil }}
			runner := jobs.Runner{Store: state, Recovery: r.recoveryJob()}
			runErr := runner.Run(ctx, outer.ID)
			retained, _, err := state.ReadInitOperation(ctx, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := state.ReadReplicaPermit(ctx, p.Database.DatabaseID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := state.GetOperation(ctx, outer.ID)
			if err != nil {
				t.Fatal(err)
			}
			unsafe := boundary == "unknown_schema" || boundary == "missing_schema_receipt"
			if unsafe {
				if runErr == nil || result.State != ops.RecoveryRequired || retained.State != "mutation_completed" || permit.FenceState != "held" {
					t.Fatalf("unsafe initialization settled: %v %+v %+v %s", runErr, result, retained, permit.FenceState)
				}
				return
			}
			want := "not_initialized"
			if boundary == "mutation_completed" {
				want = "succeeded"
			}
			if runErr != nil || result.State != ops.Succeeded || retained.State != want || permit.FenceState == "held" {
				t.Fatalf("detached recovery did not settle: %v outer=%s inner=%s fence=%s", runErr, result.State, retained.State, permit.FenceState)
			}
			sourceAfter, err := state.GetOperation(ctx, source.ID)
			if err != nil || sourceAfter.State != ops.RecoveryRequired {
				t.Fatal("source receipt rewritten", sourceAfter, err)
			}
			if _, err = state.ClaimInitialization(ctx, p); err == nil {
				t.Fatal("consumed initialization attempt reused")
			}
			report, err := r.Reconcile(ctx)
			if err != nil || len(report.Outcomes) != 0 {
				t.Fatal("repeated recovery changed settlement", report, err)
			}
		})
	}
}

type initializationMissingReceipt struct{ datainit.Journal }

func (initializationMissingReceipt) LoadInitRestorePoint(context.Context, datainit.Operation) (datainit.VerifiedRestorePoint, error) {
	return datainit.VerifiedRestorePoint{}, datainit.ErrRecovery
}
