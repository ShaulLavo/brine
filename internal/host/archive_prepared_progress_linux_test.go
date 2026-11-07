//go:build linux

package host

import (
	"context"
	"fmt"
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/store"
)

type removalInterrupt struct {
	*store.Store
	stage archive.Stage
}

func (j removalInterrupt) ClaimRemoval(ctx context.Context, p archive.Plan, task string, hold archive.SourceHold) (archive.Operation, error) {
	o, err := j.Store.ClaimRemoval(ctx, p, task, hold)
	if err == nil && j.stage == archive.Intent {
		err = archive.ErrRecovery
	}
	return o, err
}
func (j removalInterrupt) AdvanceArchive(ctx context.Context, o archive.Operation, stage archive.Stage) (archive.Operation, error) {
	o, err := j.Store.AdvanceArchive(ctx, o, stage)
	if err == nil && j.stage == stage {
		err = archive.ErrRecovery
	}
	return o, err
}
func (j removalInterrupt) SaveArchiveCoverage(ctx context.Context, o archive.Operation, c []archive.Coverage) (archive.Operation, error) {
	o, err := j.Store.SaveArchiveCoverage(ctx, o, c)
	if err == nil && j.stage == archive.CoverageVerified {
		err = archive.ErrRecovery
	}
	return o, err
}
func (j removalInterrupt) SaveArchiveManifest(ctx context.Context, o archive.Operation, m archive.Manifest) (archive.Operation, error) {
	o, err := j.Store.SaveArchiveManifest(ctx, o, m)
	if err == nil && j.stage == archive.Prepared {
		err = archive.ErrRecovery
	}
	return o, err
}

func TestRemovalPreparationProgressOneThroughSevenAbandonsWithoutManifest(t *testing.T) {
	for i := uint64(1); i <= 7; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) { testRemovalProgress(t, i) })
	}
}
func testRemovalProgress(t *testing.T, sequence uint64) {
	r := newRemovalHostRig(t)
	stage, _ := archive.StageAt(sequence)
	r.service.Journal = removalInterrupt{Store: r.prep.state, stage: stage}
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err == nil {
		t.Fatal("fault missed")
	}
	r.resolve(t, "abandon")
	inspection, err := r.service.Inspect(t.Context(), r.plan.ArchiveID)
	if err != nil || inspection.Preparation == nil || inspection.Preparation.Sequence != sequence || inspection.Preparation.Manifest || !inspection.Preparation.CanAbandon || inspection.CanReconcile {
		t.Fatal(inspection, err)
	}
	if _, err = r.service.Reconcile(t.Context(), r.plan.ArchiveID); err == nil {
		t.Fatal("continued partial preparation")
	}
	d, err := r.service.Abandon(t.Context(), r.plan.ArchiveID)
	if err != nil || d.Stage != archive.Abandoned {
		t.Fatal("abandon", d, err)
	}
	receipt := r.receipt(t)
	if receipt.ManifestSHA256 != "" || receipt.OperationID == receipt.AttemptOperationID {
		t.Fatal("fabricated manifest or original result", receipt)
	}
	r.noWriterEffects(t)
}
