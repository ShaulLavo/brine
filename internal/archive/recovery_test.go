package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type recoveryRig struct {
	*dispositionRig
	unknown bool
	checks  int
}

func (r *recoveryRig) InspectArchiveRuntime(context.Context, Plan) (RuntimeInspection, error) {
	if r.unknown {
		return RuntimeInspection{}, ErrRefused
	}
	return RuntimeInspection{Complete: r.cleanup > 0}, nil
}
func (r *recoveryRig) ReconcileArchiveRuntime(ctx context.Context, p Plan) error {
	if r.cleanup > 0 {
		return nil
	}
	return r.RetireArchiveRuntime(ctx, p)
}
func (r *recoveryRig) CheckArchiveRecovery(_ context.Context, p Plan, o Operation, m Manifest) error {
	r.checks++
	if r.failCheck || r.writersRunning || r.replicasRunning {
		return ErrRefused
	}
	return nil
}
func recoveryFixture(t *testing.T) (Service, *recoveryRig) {
	t.Helper()
	s, r := dispositionFixture(t)
	h := &recoveryRig{dispositionRig: r}
	s.Host = h
	return s, h
}
func TestArchiveReconcileEachRetirementBoundary(t *testing.T) {
	for _, stage := range []DispositionStage{RuntimeRetired, RenameIntent, Renamed, Retired} {
		t.Run(string(stage), func(t *testing.T) {
			s, r := recoveryFixture(t)
			r.failStage = stage
			if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
				t.Fatal(err)
			}
			before := r.state
			inspected, err := s.Inspect(context.Background(), r.p.ArchiveID)
			if err != nil || inspected.Disposition != before || r.state != before {
				t.Fatal("inspection changed journal", inspected, err)
			}
			r.failStage = ""
			// Fresh independent evidence must allow adoption after the initial five-minute window.
			s.Now = func() time.Time { return time.Now().Add(10 * time.Minute) }
			out, err := s.Reconcile(context.Background(), r.p.ArchiveID)
			if err != nil || out.Stage != Retired || r.cleanup != 1 || r.checks == 0 {
				t.Fatal(out, err, r.cleanup, r.checks)
			}
			if err = data.VerifyArchivedTree(r.p.Trees[0], r.p.Candidate.Incarnation, r.p.ArchiveID); err != nil {
				t.Fatal(err)
			}
			before = r.state
			if _, err = s.Reconcile(context.Background(), r.p.ArchiveID); err != nil || r.state != before || r.cleanup != 1 {
				t.Fatal("terminal replay", err)
			}
		})
	}
}
func TestArchiveReconcileRefusesUnknownRuntimeAndReplacedTree(t *testing.T) {
	for _, failure := range []string{"unknown", "writer", "replica", "ownership", "missing", "duplicate"} {
		t.Run(failure, func(t *testing.T) {
			s, r := recoveryFixture(t)
			r.failStage = Renamed
			if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
				t.Fatal(err)
			}
			before := r.state
			tree := r.p.Trees[0]
			switch failure {
			case "unknown":
				r.unknown = true
			case "writer":
				r.writersRunning = true
			case "replica":
				r.replicasRunning = true
			case "ownership":
				r.failCheck = true
			case "missing":
				if err := os.Rename(filepath.Join(string(tree.Root), tree.ArchiveRelative), filepath.Join(string(tree.Root), "saved")); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				if err := os.MkdirAll(filepath.Join(string(tree.Root), tree.SourceRelative), 0700); err != nil {
					t.Fatal(err)
				}
			}
			r.failStage = ""
			if _, err := s.Reconcile(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.state != before || r.cleanup != 1 {
				t.Fatal("unsafe adoption", err, r.state)
			}
		})
	}
}

func TestArchiveReconcilePartialMultiRootMovement(t *testing.T) {
	for _, outOfOrder := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "unjournaled next tree"}[outOfOrder], func(t *testing.T) {
			s, r := preparationFixture(t)
			d := r.p.Candidate.Databases[0]
			d.sqlite.Binding.DatabaseID = data.DatabaseID("99999999999999999999999999999999")
			d.sqlite.Binding.ReplicaBindingID = data.ReplicaBindingID("88888888888888888888888888888888")
			d.sqlite.Binding.Name = "second"
			d.sqlite.Binding.MountPath = "/second"
			d.sqlite.Binding.Root = data.PersistentRoot(t.TempDir())
			// #nosec G302 -- Private fixture root requires owner traversal.
			if err := os.Chmod(string(d.sqlite.Binding.Root), 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			d.sqlite.Binding.RelativeDirectory, err = data.RelativeDirectory(d.sqlite.Binding.IncarnationID, d.sqlite.Binding.DatabaseID)
			if err != nil {
				t.Fatal(err)
			}
			d.sqlite.Epochs = append([]data.ReplicaBinding(nil), d.sqlite.Epochs...)
			d.sqlite.Epochs[0].DatabaseID = d.sqlite.Binding.DatabaseID
			d.sqlite.Epochs[0].BindingID = d.sqlite.Binding.ReplicaBindingID
			d.sqlite.Epochs[0].EpochID = data.ReplicaEpochID("77777777777777777777777777777777")
			d.sqlite.Epochs[0].RemotePrefix, err = data.RemotePrefix(d.sqlite.Epochs[0].Destination.BasePrefix, d.sqlite.Binding.IncarnationID, d.sqlite.Binding.DatabaseID, d.sqlite.Epochs[0].EpochID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Join(string(d.sqlite.Binding.Root), d.sqlite.Binding.RelativeDirectory), 0700); err != nil {
				t.Fatal(err)
			}
			allocation, err := data.CaptureAllocation(d.sqlite.Binding)
			if err != nil {
				t.Fatal(err)
			}
			d.sqlite.Allocation = &allocation
			tree, err := data.CaptureArchiveTree(d.sqlite.Binding.Root, r.p.Candidate.Incarnation, r.p.ArchiveID)
			if err != nil {
				t.Fatal(err)
			}
			r.p.Candidate.Databases = append(r.p.Candidate.Databases, d)
			r.p.Trees = append(r.p.Trees, tree)
			slices.SortFunc(r.p.Candidate.Databases, func(a, b Database) int {
				return strings.Compare(string(a.sqlite.Binding.DatabaseID), string(b.sqlite.Binding.DatabaseID))
			})
			slices.SortFunc(r.p.Trees, func(a, b data.ArchiveTree) int { return strings.Compare(string(a.Root), string(b.Root)) })
			r.p.ID, err = r.p.Fingerprint()
			if err != nil || !r.p.Valid() {
				t.Fatal("invalid multiroot plan", err)
			}
			if _, err = s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); err != nil {
				t.Fatal(err)
			}
			rig := &recoveryRig{dispositionRig: &dispositionRig{preparationRig: r, failStage: Renamed}}
			s.Host = rig
			s.Journal = rig
			if _, err = s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
				t.Fatal(err)
			}
			before := rig.state
			if outOfOrder {
				if err = data.RenameArchiveTree(r.p.Trees[1], r.p.Candidate.Incarnation, r.p.ArchiveID); err != nil {
					t.Fatal(err)
				}
			}
			rig.failStage = ""
			out, err := s.Reconcile(context.Background(), r.p.ArchiveID)
			if outOfOrder {
				if !errors.Is(err, ErrRecovery) || rig.state != before {
					t.Fatal("unjournaled movement adopted", out, err)
				}
				return
			}
			if err != nil || out.Stage != Retired || rig.cleanup != 1 {
				t.Fatal(out, err)
			}
			for _, tree := range r.p.Trees {
				if err = data.VerifyArchivedTree(tree, r.p.Candidate.Incarnation, r.p.ArchiveID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestArchiveRecoveryRefusesInvalidTreeIndexWithoutEffects(t *testing.T) {
	s, r := recoveryFixture(t)
	r.failStage = Renamed
	if _, err := s.Retire(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) {
		t.Fatal(err)
	}
	r.state.TreeIndex = len(r.p.Trees)
	before := r.state
	inspected, err := s.Inspect(context.Background(), r.p.ArchiveID)
	if err != nil || inspected.CanReconcile || r.state != before {
		t.Fatal("invalid journal admitted", inspected, err)
	}
	if _, err = s.Reconcile(context.Background(), r.p.ArchiveID); !errors.Is(err, ErrRecovery) || r.state != before || r.cleanup != 1 {
		t.Fatal("invalid journal changed effects", err, r.state)
	}
}
