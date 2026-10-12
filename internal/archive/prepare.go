package archive

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type Journal interface {
	SaveArchivePlan(context.Context, Plan) error
	LoadArchivePlan(context.Context, string) (Plan, error)
	ReadArchiveOperation(context.Context, string) (Operation, bool, error)
	ClaimArchive(context.Context, Plan) (Operation, error)
	AdvanceArchive(context.Context, Operation, Stage) (Operation, error)
	SaveArchiveCoverage(context.Context, Operation, []Coverage) (Operation, error)
	SaveArchiveManifest(context.Context, Operation, Manifest) (Operation, error)
	ReadArchiveManifest(context.Context, data.ArchiveID) (Manifest, error)
}

type Host interface {
	Facts(context.Context, string) (Plan, error)
	StopWriters(context.Context, Plan) error
	WritersStopped(context.Context, Plan) (bool, error)
	Cover(context.Context, Plan, Database, string, time.Time) (Coverage, error)
	StopReplicas(context.Context, Plan) error
	ReplicasStopped(context.Context, Plan) (bool, error)
	AcquireLifetimeLocks(context.Context, Plan) (func() error, error)
}

type Service struct {
	Journal   Journal
	Host      Host
	Requester string
	Authorize func(context.Context) error
	Lock      func(context.Context) (func() error, error)
	Now       func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) guard(ctx context.Context) (func() error, error) {
	if s.Journal == nil || s.Host == nil || s.Authorize == nil || s.Lock == nil || !requesterName.MatchString(s.Requester) || s.Authorize(ctx) != nil {
		return nil, ErrRefused
	}
	return s.Lock(ctx)
}
func (s Service) Plan(ctx context.Context, app string) (p Plan, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !appName.MatchString(app) {
		return p, ErrRefused
	}
	release, err := s.guard(ctx)
	if err != nil {
		return p, err
	}
	defer func() { err = errors.Join(err, release()) }()
	p, err = s.Host.Facts(ctx, app)
	if err != nil || !preparationCoverageSupported(p.Candidate) {
		return Plan{}, ErrRefused
	}
	id, err := data.NewID()
	if err != nil {
		return Plan{}, err
	}
	p.ArchiveID = data.ArchiveID(id)
	p.Requester = s.Requester
	p.CreatedAt = s.now()
	roots := map[data.PersistentRoot]bool{}
	p.Trees = nil
	for _, database := range p.Candidate.Databases {
		if roots[database.Reference().Root] {
			continue
		}
		roots[database.Reference().Root] = true
		tree, err := data.CaptureArchiveTree(database.Reference().Root, p.Candidate.Incarnation, p.ArchiveID)
		if err != nil {
			return Plan{}, ErrRefused
		}
		p.Trees = append(p.Trees, tree)
	}
	slices.SortFunc(p.Trees, func(a, b data.ArchiveTree) int {
		if a.Root < b.Root {
			return -1
		}
		if a.Root > b.Root {
			return 1
		}
		return 0
	})
	p.ID, err = p.Fingerprint()
	if err != nil || !p.Valid() {
		return Plan{}, ErrRefused
	}
	return p, s.Journal.SaveArchivePlan(ctx, p)
}

func (s Service) Prepare(ctx context.Context, app, id string) (manifest Manifest, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if !appName.MatchString(app) || !digest.MatchString(id) {
		return manifest, ErrRefused
	}
	release, err := s.guard(ctx)
	if err != nil {
		return manifest, err
	}
	defer func() { err = errors.Join(err, release()) }()
	p, err := s.Journal.LoadArchivePlan(ctx, id)
	if err != nil || !p.Valid() || p.Requester != s.Requester || p.Candidate.App != app {
		return manifest, ErrRefused
	}
	old, exists, err := s.Journal.ReadArchiveOperation(ctx, id)
	if err != nil {
		return manifest, err
	}
	if exists {
		if old.Stage != Prepared {
			return manifest, ErrRecovery
		}
		return s.Journal.ReadArchiveManifest(ctx, p.ArchiveID)
	}
	fresh, err := s.Host.Facts(ctx, app)
	if err != nil || !preparationCoverageSupported(fresh.Candidate) || !reflect.DeepEqual(fresh.Candidate, p.Candidate) || fresh.PolicyHash != p.PolicyHash || fresh.TargetHash != p.TargetHash || fresh.Generation != p.Generation {
		return manifest, ErrRefused
	}
	for _, tree := range p.Trees {
		observed, err := data.CaptureArchiveTree(tree.Root, p.Candidate.Incarnation, p.ArchiveID)
		if err != nil || observed != tree {
			return manifest, ErrRefused
		}
	}
	for _, database := range p.Candidate.Databases {
		for i, epoch := range database.ArchiveBindings() {
			if !database.Retention()[i].Admits(epoch.Destination, s.now()) {
				return manifest, ErrRefused
			}
		}
	}
	op, err := s.Journal.ClaimArchive(ctx, p)
	if err != nil {
		return manifest, err
	}
	advance := func(stage Stage) error { var e error; op, e = s.Journal.AdvanceArchive(ctx, op, stage); return e }
	if advance(WritersStopIntent) != nil || s.Host.StopWriters(ctx, p) != nil {
		return manifest, ErrRecovery
	}
	stopped, err := s.Host.WritersStopped(ctx, p)
	if err != nil || !stopped || ctx.Err() != nil || advance(WritersStopped) != nil {
		return manifest, ErrRecovery
	}
	writersAt := s.now()
	if advance(CoverageIntent) != nil {
		return manifest, ErrRecovery
	}
	coverage := make([]Coverage, 0, len(p.Candidate.Databases))
	for _, database := range p.Candidate.Databases {
		point, err := s.Host.Cover(ctx, p, database, op.ID, writersAt)
		if err != nil || !point.Admits(p, database, op.ID, s.now()) {
			return manifest, ErrRecovery
		}
		coverage = append(coverage, point)
	}
	op, err = s.Journal.SaveArchiveCoverage(ctx, op, coverage)
	if err != nil || advance(ReplicaStopIntent) != nil || s.Host.StopReplicas(ctx, p) != nil {
		return manifest, ErrRecovery
	}
	stopped, err = s.Host.ReplicasStopped(ctx, p)
	if err != nil || !stopped {
		return manifest, ErrRecovery
	}
	unlock, err := s.Host.AcquireLifetimeLocks(ctx, p)
	if err != nil {
		return manifest, ErrRecovery
	}
	defer func() { err = errors.Join(err, unlock()) }()
	stopped, err = s.Host.WritersStopped(ctx, p)
	if err != nil || !stopped || ctx.Err() != nil {
		return manifest, ErrRecovery
	}
	for _, tree := range p.Trees {
		observed, err := data.CaptureArchiveTree(tree.Root, p.Candidate.Incarnation, p.ArchiveID)
		if err != nil || observed != tree {
			return manifest, ErrRecovery
		}
	}
	for _, database := range p.Candidate.Databases {
		sqlDatabase, sqlite := database.SQLite()
		if !sqlite || sqlDatabase.Allocation == nil || data.VerifyArchiveAllocation(sqlDatabase.Binding, *sqlDatabase.Allocation) != nil {
			return manifest, ErrRecovery
		}
	}
	if advance(Quiesced) != nil {
		return manifest, ErrRecovery
	}
	manifest = Manifest{Plan: p, OperationID: op.ID, Coverage: coverage, QuiescedAt: s.now(), PreparedAt: s.now()}
	if !manifest.Valid() {
		return Manifest{}, ErrRecovery
	}
	if _, err = s.Journal.SaveArchiveManifest(ctx, op, manifest); err != nil {
		return Manifest{}, ErrRecovery
	}
	return manifest, nil
}

// Initialized coverage currently restores one exact epoch source. Reject known
// unsupported histories before recording a plan or claiming any startup fences.
// Never-written allocations use fresh emptiness evidence, not an epoch restore.
func preparationCoverageSupported(candidate Candidate) bool {
	for _, database := range candidate.Databases {
		if database.Engine() == data.EngineSQLite && database.Written() && len(database.ArchiveBindings()) != 1 {
			return false
		}
	}
	return true
}
