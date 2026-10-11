package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type dispositionRig struct {
	*preparationRig
	state                  Disposition
	failCheck              bool
	cleanup                int
	failStage              DispositionStage
	resumed                string
	failPreflight          bool
	failRuntimeAfterIntent bool
	failCleanup            bool
	writersRunning         bool
	replicasRunning        bool
}

func (r *dispositionRig) ReadArchiveDisposition(context.Context, data.ArchiveID) (Disposition, bool, error) {
	return r.state, r.state.Sequence != 0, nil
}
func (r *dispositionRig) AdvanceArchiveDisposition(_ context.Context, o Operation, old Disposition, next Disposition) (Disposition, error) {
	if !reflect.DeepEqual(old, r.state) || !DispositionNext(old, next, len(r.p.Trees)) {
		return old, ErrRefused
	}
	if next.Stage == r.failStage {
		return old, ErrRecovery
	}
	r.state = next
	return next, nil
}
func (r *dispositionRig) FinishArchiveDisposition(_ context.Context, _ Operation, old Disposition, next Disposition) (Disposition, error) {
	if !reflect.DeepEqual(old, r.state) || !DispositionNext(old, next, len(r.p.Trees)) {
		return old, ErrRefused
	}
	if next.Stage == r.failStage {
		return old, ErrRecovery
	}
	r.state = next
	return next, nil
}
func (r *dispositionRig) LoadArchivePlanByArchive(_ context.Context, id data.ArchiveID) (Plan, error) {
	if id != r.p.ArchiveID {
		return Plan{}, ErrRefused
	}
	return r.p, nil
}
func (r *dispositionRig) VerifyArchiveRuntime(context.Context, Plan) error {
	if r.failPreflight || r.cleanup != 0 || r.failRuntimeAfterIntent && r.state.Stage == AbandonIntent {
		return ErrRefused
	}
	return nil
}
func (r *dispositionRig) StopWriters(ctx context.Context, p Plan) error {
	if err := r.preparationRig.StopWriters(ctx, p); err != nil {
		return err
	}
	r.writersRunning = false
	return nil
}
func (r *dispositionRig) StopReplicas(ctx context.Context, p Plan) error {
	if err := r.preparationRig.StopReplicas(ctx, p); err != nil {
		return err
	}
	r.replicasRunning = false
	return nil
}
func (r *dispositionRig) WritersStopped(ctx context.Context, p Plan) (bool, error) {
	if r.writersRunning || r.state.Stage == AbandonIntent && r.resumed == "writer" {
		return false, nil
	}
	return r.preparationRig.WritersStopped(ctx, p)
}
func (r *dispositionRig) ReplicasStopped(ctx context.Context, p Plan) (bool, error) {
	if r.replicasRunning || r.state.Stage == AbandonIntent && r.resumed == "replica" {
		return false, nil
	}
	return r.preparationRig.ReplicasStopped(ctx, p)
}
func (r *dispositionRig) RecheckArchive(context.Context, Plan, Operation, bool) error {
	if r.failCheck {
		return ErrRefused
	}
	return nil
}
func (r *dispositionRig) RetireArchiveRuntime(context.Context, Plan) error {
	if r.failCleanup {
		return ErrRefused
	}
	r.cleanup++
	return nil
}
func (r *dispositionRig) ArchiveRuntimeStopped(context.Context, Plan) (bool, error) { return true, nil }
func dispositionFixture(t *testing.T) (Service, *dispositionRig) {
	t.Helper()
	s, r := preparationFixture(t)
	if _, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); err != nil {
		t.Fatal(err)
	}
	d := &dispositionRig{preparationRig: r}
	s.Journal = d
	s.Host = d
	return s, d
}
func TestArchiveAbandonRequiresFreshStateAndNeverRenames(t *testing.T) {
	s, r := dispositionFixture(t)
	r.failCheck = true
	if _, err := s.Abandon(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRefused) || r.state.Sequence != 0 {
		t.Fatal("unverified fence release", err, r.state)
	}
	r.failCheck = false
	d, err := s.Abandon(context.Background(), r.p.ArchiveID)
	if err != nil || d.Stage != Abandoned || r.cleanup != 0 {
		t.Fatal(err, d)
	}
	if _, err = os.Stat(filepath.Join(string(r.p.Trees[0].Root), r.p.Trees[0].SourceRelative)); err != nil {
		t.Fatal(err)
	}
}
func TestArchiveRetireRenamesOnlyRecordedTree(t *testing.T) {
	s, r := dispositionFixture(t)
	d, err := s.Retire(context.Background(), r.p.ArchiveID)
	if err != nil || d.Stage != Retired || r.cleanup != 1 || d.RetainUntil.Sub(d.CommittedAt) != 30*24*time.Hour {
		t.Fatal(err, d)
	}
	tree := r.p.Trees[0]
	if _, err = os.Stat(filepath.Join(string(tree.Root), tree.SourceRelative)); !os.IsNotExist(err) {
		t.Fatal("source not moved", err)
	}
	if err = data.VerifyArchivedTree(tree, r.p.Candidate.Incarnation, r.p.ArchiveID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Abandon(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
		t.Fatal("retired archive abandoned", err)
	}
}
func TestArchiveStalePreparationCanAbandonButCannotRetire(t *testing.T) {
	s, r := dispositionFixture(t)
	s.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRefused) || r.state.Sequence != 0 {
		t.Fatal(err, r.state)
	}
	if _, err := s.Abandon(context.Background(), r.p.ArchiveID); err != nil {
		t.Fatal("stale preparation stranded operator", err)
	}
}

func TestArchiveDispositionInterruptedEffectsNeverReplay(t *testing.T) {
	for _, boundary := range []DispositionStage{RuntimeRetired, RenameIntent, Renamed, Retired} {
		t.Run(string(boundary), func(t *testing.T) {
			s, r := dispositionFixture(t)
			r.failStage = boundary
			if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
				t.Fatal("interruption not retained", err)
			}
			effects := r.cleanup
			r.failStage = ""
			if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.cleanup != effects {
				t.Fatal("uncertain cleanup/rename replayed", err)
			}
			if _, err := s.Abandon(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
				t.Fatal("partial retirement released ownership", err)
			}
		})
	}
}

func TestArchiveAbandonRechecksQuiescenceAfterIntent(t *testing.T) {
	for _, process := range []string{"writer", "replica"} {
		t.Run(process, func(t *testing.T) {
			s, r := dispositionFixture(t)
			r.resumed = process
			if _, err := s.Abandon(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.state.Stage != AbandonIntent {
				t.Fatal("released fences without fresh quiescence", err, r.state)
			}
		})
	}
}

func TestArchiveAbandonEveryPreManifestStage(t *testing.T) {
	for sequence := uint64(1); sequence < 8; sequence++ {
		stage, ok := StageAt(sequence)
		if !ok {
			t.Fatal(sequence)
		}
		t.Run(string(stage), func(t *testing.T) {
			s, r := dispositionFixture(t)
			r.op.Stage = stage
			r.op.Sequence = sequence
			r.m = Manifest{}
			r.writersRunning = true
			r.replicasRunning = true
			d, err := s.Abandon(context.Background(), r.p.ArchiveID)
			if err != nil || d.Stage != Abandoned || r.cleanup != 0 || r.writersRunning || r.replicasRunning {
				t.Fatal("interrupted preparation stranded", err, d)
			}
			if err := data.VerifyUnarchivedTree(r.p.Trees[0], r.p.Candidate.Incarnation, r.p.ArchiveID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestArchiveAbandonResumesOwnInterruptedIntent(t *testing.T) {
	s, r := dispositionFixture(t)
	r.m = Manifest{}
	r.op.Stage = WritersStopped
	r.op.Sequence = 3
	r.state = Disposition{ArchiveID: r.p.ArchiveID, Sequence: 1, Stage: AbandonIntent}
	if d, err := s.Abandon(context.Background(), r.p.ArchiveID); err != nil || d.Stage != Abandoned {
		t.Fatal(err, d)
	}
}
func TestArchiveCleanupPreconditionsAndSafePreEffectExit(t *testing.T) {
	s, r := dispositionFixture(t)
	r.failPreflight = true
	if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRefused) || r.state.Sequence != 0 || r.cleanup != 0 {
		t.Fatal(err, r.state)
	}
	r.failPreflight = false
	r.failCleanup = true
	if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.state.Stage != CleanupIntent || r.cleanup != 0 {
		t.Fatal(err, r.state)
	}
	if d, err := s.Abandon(context.Background(), r.p.ArchiveID); err != nil || d.Stage != Abandoned {
		t.Fatal("pre-effect failure stranded app", err, d)
	}
}

func TestArchiveAbandonRechecksRuntimeOwnershipAfterIntent(t *testing.T) {
	s, r := dispositionFixture(t)
	r.failRuntimeAfterIntent = true
	out, err := s.Abandon(context.Background(), r.p.ArchiveID)
	if !errors.Is(err, ErrRecovery) || out.Stage != AbandonIntent || r.state.Stage != AbandonIntent {
		t.Fatal("changed artifact ownership released fences", err, out)
	}
}
