//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestSQLiteRetireConnectedPreparationRecovery(t *testing.T) {
	for _, stage := range []archive.Stage{archive.Intent, archive.WritersStopIntent, archive.WritersStopped, archive.CoverageIntent, archive.CoverageVerified, archive.ReplicaStopIntent, archive.Quiesced, archive.Prepared} {
		t.Run(string(stage), func(t *testing.T) { testConnectedPreparationRecovery(t, stage) })
	}
}
func testConnectedPreparationRecovery(t *testing.T, stage archive.Stage) {
	source := newUnclaimedRemovalHostRigSource(t, false, false)
	source.service.Journal = removalInterrupt{Store: source.prep.state, stage: stage}
	r := connectRemovalRig(t, source)
	p := r.plan(t)
	original := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err == nil {
		t.Fatal("interruption missed")
	}
	if r.status(t, original.OperationID).Operation.State != ops.RecoveryRequired {
		t.Fatal("claim lost its recovery state")
	}
	inspection := r.call(t, "sqlite_retire_inspect", dispatch.SQLiteRetireInspectArgs{ArchiveID: p.Archive.ArchiveID}).Data.(archive.Inspection)
	if inspection.Preparation == nil || inspection.Preparation.Stage != stage || inspection.CanReconcile != (stage == archive.Prepared) || !inspection.Preparation.CanAbandon {
		t.Fatal(inspection)
	}
	disposition := "abandon"
	if stage == archive.Prepared {
		disposition = "continue"
	}
	resolver := r.resolve(t, original.OperationID, disposition)
	if err := r.runtime.Runner.Run(t.Context(), resolver.OperationID); err != nil {
		t.Fatal("resolver", err)
	}
	r.checkResolved(t, original.OperationID, resolver.OperationID, disposition)
}
func (r *connectedRemovalRig) resolve(t *testing.T, original, disposition string) jobs.Accepted {
	t.Helper()
	return r.call(t, "sqlite_retire_resolve", dispatch.SQLiteRetireResolveArgs{OperationID: original, Disposition: disposition, IdempotencyKey: r.source.prep.key()}).Data.(jobs.Accepted)
}
func (r *connectedRemovalRig) checkResolved(t *testing.T, original, resolver, disposition string) {
	t.Helper()
	status := r.status(t, resolver)
	if status.Operation.State != ops.Succeeded || status.Outcome == nil {
		t.Fatal(status)
	}
	receipt, err := archive.DecodeRemovalReceipt(status.Outcome.Receipt)
	if err != nil || receipt.AttemptOperationID != original || receipt.OperationID != resolver {
		t.Fatal(receipt, err)
	}
	if disposition == "abandon" && receipt.Outcome != "abandoned" || disposition == "continue" && receipt.Outcome != "retired" {
		t.Fatal(receipt)
	}
	if r.status(t, original).Operation.State != ops.RecoveryRequired {
		t.Fatal("rewrote original history")
	}
	r.source.noWriterEffects(t)
}
func TestSQLiteRetireConnectedDispositionRecovery(t *testing.T) {
	for _, stage := range []archive.DispositionStage{archive.CleanupIntent, archive.RuntimeRetired, archive.RenameIntent, archive.Renamed} {
		t.Run(string(stage), func(t *testing.T) { testConnectedDispositionRecovery(t, stage) })
	}
}
func testConnectedDispositionRecovery(t *testing.T, stage archive.DispositionStage) {
	source := newUnclaimedRemovalHostRigSource(t, false, false)
	source.service.Journal = removalDispositionInterrupt{Store: source.prep.state, stage: stage}
	r := connectRemovalRig(t, source)
	p := r.plan(t)
	original := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err == nil {
		t.Fatal("interruption missed")
	}
	inspection := r.call(t, "sqlite_retire_inspect", dispatch.SQLiteRetireInspectArgs{ArchiveID: p.Archive.ArchiveID}).Data.(archive.Inspection)
	if inspection.Disposition.Stage != stage || !inspection.CanReconcile {
		t.Fatal(inspection)
	}
	resolver := r.resolve(t, original.OperationID, "continue")
	if err := r.runtime.Runner.Run(t.Context(), resolver.OperationID); err != nil {
		t.Fatal(err)
	}
	r.checkResolved(t, original.OperationID, resolver.OperationID, "continue")
}
func TestSQLiteRetireConnectedLostCompletionReply(t *testing.T) {
	r := connectRemovalRig(t, newUnclaimedRemovalHostRigSource(t, false, false))
	p := r.plan(t)
	original := r.apply(t, p)
	handler := r.runtime.Runner.TaskHandlers[ops.SQLiteRetire]
	calls := 0
	r.runtime.Runner.TaskHandlers[ops.SQLiteRetire] = func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		calls++
		_, err := handler(ctx, op)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("reply lost after committed receipt")
	}
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err == nil {
		t.Fatal("fault missed")
	}
	status := r.status(t, original.OperationID)
	if status.Operation.State != ops.Succeeded {
		t.Fatal(status)
	}
	before := string(status.Outcome.Receipt)
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err != nil {
		t.Fatal("joined readback", err)
	}
	if calls != 1 || string(r.status(t, original.OperationID).Outcome.Receipt) != before {
		t.Fatal("replayed committed work")
	}
}
func TestSQLiteRetireConnectedUnclaimedFailure(t *testing.T) {
	r := connectRemovalRig(t, newUnclaimedRemovalHostRigSource(t, false, false))
	p := r.plan(t)
	accepted := r.apply(t, p)
	r.runtime.Runner.TaskHandlers[ops.SQLiteRetire] = func(context.Context, ops.Operation) (json.RawMessage, error) { return nil, context.Canceled }
	if err := r.runtime.Runner.Run(t.Context(), accepted.OperationID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.status(t, accepted.OperationID).Operation.State != ops.Failed {
		t.Fatal("unclaimed task not failed")
	}
	if _, exists, err := r.source.prep.state.ReadArchiveOperation(t.Context(), p.PlanID); err != nil || exists {
		t.Fatal("unclaimed failure acquired archive", err)
	}
	if err := r.source.prep.state.CompleteTask(t.Context(), accepted.OperationID, ops.Succeeded, ops.TaskOutcome{}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("generic task completion admitted", err)
	}
}

func TestSQLiteRetireConnectedResolverLostReplyAndOldHistory(t *testing.T) {
	source := newUnclaimedRemovalHostRigSource(t, false, false)
	source.service.Journal = removalInterrupt{Store: source.prep.state, stage: archive.Quiesced}
	r := connectRemovalRig(t, source)
	p := r.plan(t)
	original := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err == nil {
		t.Fatal("fault missed")
	}
	resolver := r.resolve(t, original.OperationID, "abandon")
	handler := r.runtime.Runner.TaskHandlers[ops.SQLiteRetireResolve]
	calls := 0
	r.runtime.Runner.TaskHandlers[ops.SQLiteRetireResolve] = func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		calls++
		_, err := handler(ctx, op)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("resolver reply lost")
	}
	if err := r.runtime.Runner.Run(t.Context(), resolver.OperationID); err == nil {
		t.Fatal("fault missed")
	}
	r.checkResolved(t, original.OperationID, resolver.OperationID, "abandon")
	if err := r.runtime.Runner.Run(t.Context(), resolver.OperationID); err != nil || calls != 1 {
		t.Fatal("resolver replayed", err, calls)
	}
	for _, ref := range source.plan.Removal.Points {
		if _, err := source.prep.state.ReadSQLitePreparedSource(t.Context(), "hello", "main", ref.PreparationID); err != nil {
			t.Fatal("old preparation unreadable", err)
		}
	}
}

func TestSQLiteRetireResolutionRefusesWrongActionBeforeSubmission(t *testing.T) {
	for _, stage := range []archive.DispositionStage{"", archive.RuntimeRetired} {
		testSQLiteRetireResolutionAction(t, stage)
	}
}
func testSQLiteRetireResolutionAction(t *testing.T, stage archive.DispositionStage) {
	source := newUnclaimedRemovalHostRigSource(t, false, false)
	source.service.Journal = removalInterrupt{Store: source.prep.state, stage: archive.Quiesced}
	if stage != "" {
		source.service.Journal = removalDispositionInterrupt{Store: source.prep.state, stage: stage}
	}
	r := connectRemovalRig(t, source)
	p := r.plan(t)
	original := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), original.OperationID); err == nil {
		t.Fatal("fault missed")
	}
	decision, code := "continue", result.ContinueUnavailable
	if stage != "" {
		decision, code = "abandon", result.RecoveryRequired
	}
	db := removalControlDB(t, source.prep.root)
	before := removalRowCount(t, db, "SELECT count(*) FROM operations")
	response, err := r.response(t, "sqlite_retire_resolve", dispatch.SQLiteRetireResolveArgs{OperationID: original.OperationID, Disposition: decision, IdempotencyKey: source.prep.key()})
	if err == nil || response.OK || result.Classify(err).Code() != code {
		t.Fatal(response, err)
	}
	if removalRowCount(t, db, "SELECT count(*) FROM operations") != before {
		t.Fatal("wrong action created a resolver")
	}
	if r.status(t, original.OperationID).Operation.State != ops.RecoveryRequired {
		t.Fatal("original history changed")
	}
	source.noWriterEffects(t)
}

func TestSQLiteRetireFailedAttemptHistorySurvivesLaterClaim(t *testing.T) {
	r := connectRemovalRig(t, newUnclaimedRemovalHostRigSource(t, false, false))
	p := r.plan(t)
	first := r.apply(t, p)
	handler := r.runtime.Runner.TaskHandlers[ops.SQLiteRetire]
	r.runtime.Runner.TaskHandlers[ops.SQLiteRetire] = func(context.Context, ops.Operation) (json.RawMessage, error) { return nil, context.Canceled }
	if err := r.runtime.Runner.Run(t.Context(), first.OperationID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.status(t, first.OperationID).Operation.State != ops.Failed {
		t.Fatal("first attempt did not fail before claim")
	}
	r.runtime.Runner.TaskHandlers[ops.SQLiteRetire] = handler
	second := r.apply(t, p)
	if err := r.runtime.Runner.Run(t.Context(), second.OperationID); err != nil {
		t.Fatal(err)
	}
	if r.status(t, second.OperationID).Operation.State != ops.Succeeded {
		t.Fatal("second attempt failed")
	}
	response, err := r.response(t, "operation", dispatch.OperationArgs{OperationID: first.OperationID})
	if err != nil || !response.OK {
		t.Fatal("later claim hid original failed history", err)
	}
	if response.Data.(jobs.Status).Operation.State != ops.Failed {
		t.Fatal("failed history changed")
	}
}
