package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type preparationRig struct {
	p      Plan
	op     Operation
	m      Manifest
	events []string
	fail   string
}

func (r *preparationRig) event(name string) error {
	r.events = append(r.events, name)
	if name == r.fail {
		return ErrRecovery
	}
	return nil
}
func (r *preparationRig) SaveArchivePlan(_ context.Context, p Plan) error       { r.p = p; return nil }
func (r *preparationRig) LoadArchivePlan(context.Context, string) (Plan, error) { return r.p, nil }
func (r *preparationRig) ReadArchiveOperation(context.Context, string) (Operation, bool, error) {
	return r.op, r.op.ID != "", nil
}
func (r *preparationRig) ClaimArchive(_ context.Context, p Plan) (Operation, error) {
	if err := r.event("intent"); err != nil {
		return Operation{}, err
	}
	r.op = Operation{ID: strings.Repeat("f", 32), PlanID: p.ID, ArchiveID: p.ArchiveID, Stage: Intent, Sequence: 1}
	return r.op, nil
}
func (r *preparationRig) AdvanceArchive(_ context.Context, o Operation, s Stage) (Operation, error) {
	if err := r.event(string(s)); err != nil {
		return o, err
	}
	o.Stage = s
	o.Sequence++
	r.op = o
	return o, nil
}
func (r *preparationRig) SaveArchiveCoverage(ctx context.Context, o Operation, _ []Coverage) (Operation, error) {
	return r.AdvanceArchive(ctx, o, CoverageVerified)
}
func (r *preparationRig) SaveArchiveManifest(ctx context.Context, o Operation, m Manifest) (Operation, error) {
	next, err := r.AdvanceArchive(ctx, o, Prepared)
	if err == nil {
		r.m = m
	}
	return next, err
}
func (r *preparationRig) ReadArchiveManifest(context.Context, data.ArchiveID) (Manifest, error) {
	return r.m, nil
}
func (r *preparationRig) Facts(context.Context, string) (Plan, error) { return r.p, r.event("facts") }
func (r *preparationRig) StopWriters(context.Context, Plan) error     { return r.event("stop writers") }
func (r *preparationRig) WritersStopped(context.Context, Plan) (bool, error) {
	err := r.event("inspect writers")
	return err == nil, err
}
func (r *preparationRig) Cover(_ context.Context, _ Plan, d Database, _ string, at time.Time) (Coverage, error) {
	fresh, observed, err := data.FreshArchiveAllocation(d.Binding, *d.Allocation)
	if err != nil {
		return Coverage{}, err
	}
	return Coverage{DatabaseID: d.Binding.DatabaseID, WritersQuiescedAt: at, Empty: &fresh, Observation: observed}, r.event("cover")
}
func (r *preparationRig) StopReplicas(context.Context, Plan) error { return r.event("stop replicas") }
func (r *preparationRig) ReplicasStopped(context.Context, Plan) (bool, error) {
	err := r.event("inspect replicas")
	return err == nil, err
}
func (r *preparationRig) AcquireLifetimeLocks(context.Context, Plan) (func() error, error) {
	if err := r.event("lock replicas"); err != nil {
		return nil, err
	}
	return func() error { return r.event("unlock replicas") }, nil
}
func preparationFixture(t *testing.T) (Service, *preparationRig) {
	t.Helper()
	p := archivePlan(t)
	root := t.TempDir()
	// #nosec G302 -- Private fixture directory requires owner traversal.
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	d := &p.Candidate.Databases[0]
	d.Binding.Root = data.PersistentRoot(root)
	if err := os.MkdirAll(filepath.Join(root, d.Binding.RelativeDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	allocation, err := data.CaptureAllocation(d.Binding)
	if err != nil {
		t.Fatal(err)
	}
	d.Allocation = &allocation
	tree, err := data.CaptureArchiveTree(d.Binding.Root, p.Candidate.Incarnation, p.ArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	p.Trees = []data.ArchiveTree{tree}
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		t.Fatal("invalid plan", err)
	}
	r := &preparationRig{p: p}
	s := Service{Journal: r, Host: r, Requester: p.Requester, Authorize: func(context.Context) error { return nil }, Lock: func(context.Context) (func() error, error) {
		return func() error { return r.event("unlock host") }, nil
	}}
	return s, r
}
func TestPreparationOrdersEvidenceAndDoesNotRename(t *testing.T) {
	s, r := preparationFixture(t)
	m, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID)
	if err != nil || !m.Valid() {
		t.Fatal("preparation", err)
	}
	want := []string{"facts", "intent", "writers_stop_intent", "stop writers", "inspect writers", "writers_stopped", "coverage_intent", "cover", "coverage_verified", "replica_stop_intent", "stop replicas", "inspect replicas", "lock replicas", "inspect writers", "quiesced", "prepared", "unlock replicas", "unlock host"}
	if !reflect.DeepEqual(r.events, want) {
		t.Fatalf("unexpected effects: %v", r.events)
	}
	source := filepath.Join(string(r.p.Trees[0].Root), r.p.Trees[0].SourceRelative)
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source changed", err)
	}
	if _, err := os.Stat(filepath.Join(string(r.p.Trees[0].Root), r.p.Trees[0].ArchiveRelative)); !os.IsNotExist(err) {
		t.Fatal("preparation renamed data")
	}
	r.events = nil
	again, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID)
	if err != nil || !reflect.DeepEqual(m, again) || !reflect.DeepEqual(r.events, []string{"unlock host"}) {
		t.Fatal("prepared retrieval replayed effects", err, r.events)
	}
}
func TestPreparationInterruptedBoundariesNeverReplay(t *testing.T) {
	for _, boundary := range []string{"writers_stop_intent", "stop writers", "inspect writers", "writers_stopped", "coverage_intent", "cover", "coverage_verified", "replica_stop_intent", "stop replicas", "inspect replicas", "lock replicas", "quiesced", "prepared"} {
		t.Run(boundary, func(t *testing.T) {
			s, r := preparationFixture(t)
			r.fail = boundary
			if _, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); !errors.Is(err, ErrRecovery) {
				t.Fatal("unknown attempt admitted", err)
			}
			if r.op.ID == "" || r.op.Stage == Prepared {
				t.Fatal("lost durable intent", r.op)
			}
			r.events = nil
			r.fail = ""
			if _, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); !errors.Is(err, ErrRecovery) || !reflect.DeepEqual(r.events, []string{"unlock host"}) {
				t.Fatal("interrupted attempt replayed", err, r.events)
			}
		})
	}
}
func TestPreparationReplacedDatabaseRefuses(t *testing.T) {
	s, r := preparationFixture(t)
	d := r.p.Candidate.Databases[0]
	path := filepath.Join(string(d.Binding.Root), d.Binding.RelativeDirectory)
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); !errors.Is(err, ErrRecovery) {
		t.Fatal("replaced database admitted", err)
	}
}

func TestArchivePlanCreatesIndependentIdentityWithoutStopping(t *testing.T) {
	s, r := preparationFixture(t)
	old := r.p.ArchiveID
	p, err := s.Plan(context.Background(), r.p.Candidate.App)
	if err != nil || !p.Valid() || p.ArchiveID == old || !reflect.DeepEqual(r.events, []string{"facts", "unlock host"}) {
		t.Fatal("plan changed host or reused identity", err, r.events)
	}
}
func TestArchiveUnauthorizedRequesterCannotTakeHostLock(t *testing.T) {
	s, r := preparationFixture(t)
	s.Authorize = func(context.Context) error { return ErrRefused }
	if _, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); !errors.Is(err, ErrRefused) || len(r.events) != 0 {
		t.Fatal("unauthorized effects", err, r.events)
	}
}
func TestArchiveLockReleaseFailureDoesNotBecomeSuccess(t *testing.T) {
	s, r := preparationFixture(t)
	r.fail = "unlock replicas"
	if _, err := s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID); !errors.Is(err, ErrRecovery) || r.op.Stage != Prepared {
		t.Fatal("lost cleanup failure or terminal evidence", err)
	}
}

func multiEpochPreparationPlan(t *testing.T, p Plan, written bool) Plan {
	t.Helper()
	p.Candidate.Databases = append([]Database(nil), p.Candidate.Databases...)
	d := &p.Candidate.Databases[0]
	d.Written = written
	epoch := d.Epochs[0]
	epoch.EpochID = data.ReplicaEpochID(strings.Repeat("f", 32))
	var err error
	epoch.RemotePrefix, err = data.RemotePrefix(epoch.Destination.BasePrefix, d.Binding.IncarnationID, d.Binding.DatabaseID, epoch.EpochID)
	if err != nil {
		t.Fatal(err)
	}
	d.Epochs = append(append([]data.ReplicaBinding(nil), d.Epochs...), epoch)
	d.Retention = append(append([]data.RetentionEvidence(nil), d.Retention...), d.Retention[0])
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		t.Fatal("invalid multi-epoch fixture", err)
	}
	return p
}

type preparationFactsRig struct {
	*preparationRig
	fresh Plan
}

func (r preparationFactsRig) Facts(context.Context, string) (Plan, error) {
	return r.fresh, r.event("facts")
}

func TestArchiveUnsupportedEpochCoverageRefusesBeforeEffects(t *testing.T) {
	for _, phase := range []string{"planning", "retained plan", "fresh facts"} {
		t.Run(phase, func(t *testing.T) {
			s, r := preparationFixture(t)
			unsupported := multiEpochPreparationPlan(t, r.p, true)
			if phase == "fresh facts" {
				s.Host = preparationFactsRig{preparationRig: r, fresh: unsupported}
			} else {
				r.p = unsupported
			}
			retained := r.p
			var err error
			if phase == "planning" {
				_, err = s.Plan(context.Background(), r.p.Candidate.App)
			} else {
				_, err = s.Prepare(context.Background(), r.p.Candidate.App, r.p.ID)
			}
			if !errors.Is(err, ErrRefused) || r.op.ID != "" || !reflect.DeepEqual(r.p, retained) || !reflect.DeepEqual(r.events, []string{"facts", "unlock host"}) {
				t.Fatal("unsupported coverage caused an outage or saved a plan", err, r.op, r.events)
			}
		})
	}
}

func TestArchiveNeverWrittenMultiEpochPlanningRemainsSupported(t *testing.T) {
	s, r := preparationFixture(t)
	r.p = multiEpochPreparationPlan(t, r.p, false)
	if _, err := s.Plan(context.Background(), r.p.Candidate.App); err != nil {
		t.Fatal(err)
	}
}
