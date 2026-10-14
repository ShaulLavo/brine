//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/datainit"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type interruptedTaskLaunch struct{}

func (interruptedTaskLaunch) Launch(context.Context, systemd.OperationID) error { return nil }
func TestPublicReconcileRecordsInterruptedNativeTaskForOriginalBackupInspection(t *testing.T) {
	state, prep, lifetime, original := nativePreparationFixture(t)
	ctx := context.Background()
	if _, err := state.RecordNativeStoppedPreparation(ctx, prep, lifetime, nativeAbsentStopFixture()); err != nil {
		t.Fatal(err)
	}
	before, err := state.ReadNativeTaskInput(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := readNativePreparationEvents(ctx, state.db, prep.intent, prep.raw)
	if err != nil {
		t.Fatal(err)
	}
	prefix, _ := json.Marshal(events)
	beforePermit, err := state.ReadEnginePermit(ctx, before.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	fencesBefore, _ := json.Marshal(beforePermit.Fences)
	if err := lifetime.Release(); err != nil {
		t.Fatal(err)
	}
	if err := prep.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	service := jobs.Service{Store: state, Launcher: interruptedTaskLaunch{}, Requester: original.Requester}
	accepted, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager := &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }}
	engine := reconcile.Reconciler{Store: state, Systemd: manager, ExcludeID: accepted.OperationID}
	runner := jobs.Runner{Store: state, Recovery: func(ctx context.Context, _ string) error { _, err := engine.Reconcile(ctx); return err }}
	if err := runner.Run(ctx, accepted.OperationID); err != nil {
		t.Fatal(err)
	}
	settled, err := state.GetOperation(ctx, original.ID)
	if err != nil || settled.State != ops.RecoveryRequired {
		t.Fatal("abandoned task was not marked unknown", err, settled.State)
	}
	outcome, err := state.ReadTaskOutcome(ctx, original.ID)
	if err != nil || outcome == nil || outcome.Error != result.RecoveryRequired || len(outcome.Receipt) != 0 {
		t.Fatal("reconciliation omitted typed unknown task outcome", err, outcome)
	}
	inspected, err := state.OpenNativeBackupInspection(ctx, original.ID, original.Requester)
	if err != nil {
		t.Fatal("public backup inspector cannot open interrupted original", err)
	}
	if inspected.OriginalOperationID() != original.ID || inspected.OriginalKind() != ops.NativePrepare || inspected.BindingID() != before.BindingID {
		t.Fatal("inspector rebound original identity")
	}
	if err := inspected.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := state.ReadNativeTaskInput(ctx, original.ID)
	if err != nil || before != after {
		t.Fatal("reconcile changed original task scope", err)
	}
	events, err = readNativePreparationEvents(ctx, state.db, prep.intent, prep.raw)
	afterPrefix, _ := json.Marshal(events)
	if err != nil || !bytes.Equal(prefix, afterPrefix) {
		t.Fatal("reconcile rewrote native preparation proof", err)
	}
	afterPermit, err := state.ReadEnginePermit(ctx, before.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	fencesAfter, _ := json.Marshal(afterPermit.Fences)
	if !bytes.Equal(fencesBefore, fencesAfter) {
		t.Fatal("reconciliation changed held original fence")
	}
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := state.ReadTaskOutcome(ctx, original.ID)
	beforeOutcome, _ := json.Marshal(outcome)
	afterOutcome, _ := json.Marshal(again)
	if err != nil || !bytes.Equal(beforeOutcome, afterOutcome) {
		t.Fatal("repeated reconcile rewrote terminal outcome", err)
	}
}

func TestNativeTaskReconcileRequiresSettledRunnerAndKnownJobAbsence(t *testing.T) {
	for _, name := range []string{"absent", "failed", "inactive", "active", "pending", "unknown-queue", "unknown-unit", "transition"} {
		t.Run(name, func(t *testing.T) {
			state, prep, lifetime, op := nativePreparationFixture(t)
			ctx := context.Background()
			if err := lifetime.Release(); err != nil {
				t.Fatal(err)
			}
			if err := prep.preparation.Close(); err != nil {
				t.Fatal(err)
			}
			manager := &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
				switch name {
				case "absent", "pending", "unknown-queue":
					return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
				case "failed":
					return systemd.Properties{ActiveState: "failed", SubState: "failed"}, nil
				case "inactive":
					return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
				case "unknown-unit":
					return systemd.Properties{}, &localexec.Error{Kind: localexec.Timeout}
				case "transition":
					return systemd.Properties{ActiveState: "deactivating", SubState: "stop"}, nil
				default:
					return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
				}
			}, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) {
				if name == "unknown-queue" {
					return false, &localexec.Error{Kind: localexec.Timeout}
				}
				return name == "pending", nil
			}}
			engine := reconcile.Reconciler{Store: state, Systemd: manager}
			if _, err := engine.DryRun(ctx); err != nil {
				t.Fatal(err)
			}
			if outcome, err := state.ReadTaskOutcome(ctx, op.ID); err != nil || outcome != nil {
				t.Fatal("preview wrote outcome", err)
			}
			if _, err := engine.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			current, err := state.GetOperation(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := state.ReadTaskOutcome(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			dead := name == "absent" || name == "failed" || name == "inactive"
			if dead && (current.State != ops.RecoveryRequired || outcome == nil || outcome.Error != result.RecoveryRequired) {
				t.Fatal("settled task lacked unknown outcome", current.State, outcome)
			}
			if !dead && (current.State != ops.Preflight || outcome != nil) {
				t.Fatal("unsettled runner was terminalized", current.State, outcome)
			}
		})
	}
}

type concurrentlyCompletedTaskStore struct{ *Store }

func (s concurrentlyCompletedTaskStore) CompleteTask(ctx context.Context, id string, state ops.State, outcome ops.TaskOutcome) error {
	if err := s.Store.CompleteTask(ctx, id, ops.Failed, ops.TaskOutcome{Error: result.PolicyRefused}); err != nil {
		return err
	}
	return s.Store.CompleteTask(ctx, id, state, outcome)
}
func TestNativeReconcileCannotReplaceConcurrentTerminalTaskOutcome(t *testing.T) {
	state, prep, lifetime, op := nativePreparationFixture(t)
	ctx := context.Background()
	if err := lifetime.Release(); err != nil {
		t.Fatal(err)
	}
	if err := prep.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	manager := &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }}
	engine := reconcile.Reconciler{Store: concurrentlyCompletedTaskStore{state}, Systemd: manager}
	if _, err := engine.Reconcile(ctx); !errors.Is(err, ops.ErrStateConflict) {
		t.Fatal("stale phase overwrote completed task", err)
	}
	current, err := state.GetOperation(ctx, op.ID)
	if err != nil || current.State != ops.Failed {
		t.Fatal("concurrent terminal state changed", err, current.State)
	}
	outcome, err := state.ReadTaskOutcome(ctx, op.ID)
	if err != nil || outcome == nil || outcome.Error != result.PolicyRefused {
		t.Fatal("concurrent outcome changed", err, outcome)
	}
}

func TestNativeCaptureReconcileRecordsUnknownWithoutInventingCaptureProof(t *testing.T) {
	state, p, other := managedControlFixture(t)
	ctx := context.Background()
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := state.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativeCapture, BindingID: p.Managed.Database.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeCapture, App: p.App, SecretRef: reference}, requester.String(), "interrupted-native-capture")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	manager := &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }}
	engine := reconcile.Reconciler{Store: state, Systemd: manager, ExcludeID: other.ID}
	if _, err := engine.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	outcome, err := state.ReadTaskOutcome(ctx, op.ID)
	if err != nil || outcome == nil || outcome.Error != result.RecoveryRequired || len(outcome.Receipt) != 0 {
		t.Fatal("capture kind lacked bounded unknown outcome", err, outcome)
	}
	if inspector, err := state.OpenNativeBackupInspection(ctx, op.ID, requester.String()); err == nil {
		_ = inspector.Close()
		t.Fatal("unknown outcome invented missing capture/upload evidence")
	}
	input, err := state.ReadNativeTaskInput(ctx, op.ID)
	if err != nil || input.Kind != ops.NativeCapture || input.BindingID != p.Managed.Database.BindingID {
		t.Fatal("capture scope changed", err, input.Kind)
	}
}
