//go:build linux

package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestRemovalPreparedStageEightContinuesWithNewRestoreOutput(t *testing.T) {
	r := newRemovalHostRig(t)
	r.service.Journal = removalInterrupt{Store: r.prep.state, stage: archive.Prepared}
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err == nil {
		t.Fatal("lost manifest reply fault missed")
	}
	r.resolve(t, "continue")
	inspected, err := r.service.Inspect(t.Context(), r.plan.ArchiveID)
	if err != nil || !inspected.CanReconcile || !inspected.Preparation.Manifest {
		t.Fatal(inspected, err)
	}
	d, err := r.service.Reconcile(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Retired {
		t.Fatal("fresh source continuation", d, err)
	}
	receipt := r.receipt(t)
	if receipt.OperationID == receipt.AttemptOperationID || receipt.OperationID != r.task.ID {
		t.Fatal(receipt)
	}
	original, err := r.prep.state.GetOperation(t.Context(), receipt.AttemptOperationID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("overwrote original recovery", original.State, err)
	}
	r.noWriterEffects(t)
}

func TestRemovalPreparedWALChangeAtFirstRenameDoesNotMoveTree(t *testing.T) {
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	r.recorder.beforeWithdraw = func() { replaceRemovalWAL(t, r) }
	if _, err := r.service.Retire(t.Context(), r.plan.ArchiveID); err == nil {
		t.Fatal("changed WAL moved")
	}
	if data.VerifyUnarchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		t.Fatal("source renamed after changed WAL")
	}
	if r.prep.heldFences() != 1 {
		t.Fatal("released original fence")
	}
	r.noWriterEffects(t)
}

type removalDispositionInterrupt struct {
	*store.Store
	stage archive.DispositionStage
}

func (j removalDispositionInterrupt) AdvanceArchiveDisposition(ctx context.Context, o archive.Operation, old, next archive.Disposition) (archive.Disposition, error) {
	d, err := j.Store.AdvanceArchiveDisposition(ctx, o, old, next)
	if err == nil && next.Stage == j.stage {
		err = archive.ErrRecovery
	}
	return d, err
}

func TestRemovalRecoveryUsesRealArchivedLocationAfterRename(t *testing.T) {
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	r.service.Journal = removalDispositionInterrupt{Store: r.prep.state, stage: archive.Renamed}
	if _, err := r.service.Retire(t.Context(), r.plan.ArchiveID); err == nil {
		t.Fatal("rename interruption missed")
	}
	if data.VerifyArchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		t.Fatal("fixture did not rename")
	}
	r.resolve(t, "continue")
	d, err := r.service.Reconcile(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Retired {
		t.Fatal("archived-source recovery", d, err)
	}
	withdrawals := 0
	for _, call := range r.recorder.calls {
		if call == "withdraw hello" {
			withdrawals++
		}
	}
	if withdrawals != 1 {
		t.Fatal("replayed route withdrawal", withdrawals)
	}
	r.noWriterEffects(t)
}

func TestRemovalRecoveryRefusesDuplicateLayoutBeforeRuntimeEffects(t *testing.T) {
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	r.resolve(t, "continue")
	if err := os.MkdirAll(filepath.Join(string(r.plan.Trees[0].Root), r.plan.Trees[0].ArchiveRelative), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.Reconcile(t.Context(), r.plan.ArchiveID); err == nil {
		t.Fatal("duplicate tree admitted")
	}
	for _, call := range r.recorder.calls {
		if call == "withdraw hello" {
			t.Fatal("withdrew route for conflicting layout")
		}
	}
	r.noWriterEffects(t)
}
