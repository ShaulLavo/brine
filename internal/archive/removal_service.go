package archive

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

// RemovalRecoveryHost returns the current isolated restore outputs. A successful
// legacy error-only check is not evidence for the protected removal journal.
type RemovalRecoveryHost interface {
	CheckRemovalRecovery(context.Context, Plan, Operation, Manifest, string) ([]restore.SQLiteReceipt, error)
	RecordRemovalRecovery(context.Context, string, Operation, []restore.SQLiteReceipt, SourceHold) error
}

type removalRun struct {
	service     Service
	plan        Plan
	operation   Operation
	disposition Disposition
	manifest    Manifest
	hold        SourceHold
	journal     DispositionJournal
	host        DispositionHost
}

func (s Service) removalPlan(ctx context.Context, id string) (Plan, bool, error) {
	if s.Journal == nil {
		return Plan{}, false, ErrRefused
	}
	p, err := s.Journal.LoadArchivePlan(ctx, id)
	return p, err == nil && p.Removal != nil, err
}

func (s Service) authorizeRemoval(ctx context.Context, p Plan) error {
	if s.Host == nil || s.Authorize == nil || !requesterName.MatchString(s.Requester) || !nativeArchiveOperationID.MatchString(s.Operation) || !p.Valid() || p.Removal == nil || p.Requester != s.Requester {
		return ErrRefused
	}
	if _, ok := s.Journal.(RemovalJournal); !ok {
		return ErrRefused
	}
	return s.Authorize(ctx)
}

func (s Service) holdRemoval(ctx context.Context, p Plan) (SourceHold, error) {
	if err := s.authorizeRemoval(ctx, p); err != nil {
		return nil, err
	}
	h, ok := s.Host.(RemovalHost)
	if !ok {
		return nil, ErrRefused
	}
	return h.HoldPrepared(ctx, p)
}

func (s Service) prepareRemoval(ctx context.Context, p Plan, app string) (m Manifest, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if app != p.Candidate.App {
		return m, ErrRefused
	}
	hold, err := s.holdRemoval(ctx, p)
	if err != nil {
		return m, err
	}
	defer func() { err = errors.Join(err, hold.Close()) }()
	old, exists, err := s.Journal.ReadArchiveOperation(ctx, p.ID)
	if err != nil {
		return m, err
	}
	if exists {
		if old.Stage != Prepared {
			return m, ErrRecovery
		}
		return s.Journal.ReadArchiveManifest(ctx, p.ArchiveID)
	}
	if err = s.checkRemovalPreparation(ctx, p, hold); err != nil {
		return m, err
	}
	o, err := s.Journal.(RemovalJournal).ClaimRemoval(ctx, p, s.Operation, hold)
	if err != nil {
		return m, err
	}
	r := removalRun{service: s, plan: p, operation: o, hold: hold}
	return r.prepare(ctx)
}

func (s Service) checkRemovalPreparation(ctx context.Context, p Plan, hold SourceHold) error {
	fresh, err := s.Host.Facts(ctx, p.Candidate.App)
	if err != nil || !reflect.DeepEqual(fresh.Candidate, p.Candidate) || fresh.PolicyHash != p.PolicyHash || fresh.TargetHash != p.TargetHash || fresh.Generation != p.Generation || fresh.Removal == nil || !reflect.DeepEqual(fresh.Removal.Points, p.Removal.Points) || fresh.Removal.Bounds != p.Removal.Bounds || !s.now().Before(p.Removal.ExpiresAt) {
		return ErrRefused
	}
	if err = verifyRemovalSources(p, false); err != nil {
		return err
	}
	if err = removalStopped(ctx, s.Host, p); err != nil {
		return err
	}
	runtime, ok := s.Host.(DispositionHost)
	if !ok || runtime.VerifyArchiveRuntime(ctx, p) != nil {
		return ErrRefused
	}
	return hold.Recheck(ctx)
}

func removalStopped(ctx context.Context, h Host, p Plan) error {
	writers, err := h.WritersStopped(ctx, p)
	if err != nil || !writers {
		return ErrRefused
	}
	replicas, err := h.ReplicasStopped(ctx, p)
	if err != nil || !replicas {
		return ErrRefused
	}
	return ctx.Err()
}

func verifyRemovalSources(p Plan, archived bool) error {
	for _, tree := range p.Trees {
		if !archived && data.VerifyUnarchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID) != nil {
			return ErrRefused
		}
		if archived && data.VerifyArchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID) != nil {
			return ErrRefused
		}
	}
	for _, d := range p.Candidate.Databases {
		if verifyDatabaseAllocation(d, p.ArchiveID, archived) != nil {
			return ErrRefused
		}
	}
	return nil
}

func (r *removalRun) prepare(ctx context.Context) (Manifest, error) {
	if r.advancePreparation(ctx, WritersStopIntent) != nil || r.service.Host.StopWriters(ctx, r.plan) != nil || removalStopped(ctx, r.service.Host, r.plan) != nil || r.advancePreparation(ctx, WritersStopped) != nil || r.advancePreparation(ctx, CoverageIntent) != nil {
		return Manifest{}, ErrRecovery
	}
	coverage, err := r.preparedCoverage(ctx)
	if err != nil {
		return Manifest{}, ErrRecovery
	}
	r.operation, err = r.service.Journal.SaveArchiveCoverage(ctx, r.operation, coverage)
	if err != nil || r.advancePreparation(ctx, ReplicaStopIntent) != nil || r.service.Host.StopReplicas(ctx, r.plan) != nil || removalStopped(ctx, r.service.Host, r.plan) != nil || r.hold.Recheck(ctx) != nil || verifyRemovalSources(r.plan, false) != nil || r.advancePreparation(ctx, Quiesced) != nil {
		return Manifest{}, ErrRecovery
	}
	m := Manifest{Plan: r.plan, OperationID: r.operation.ID, Coverage: coverage, QuiescedAt: r.service.now(), PreparedAt: r.service.now()}
	if !m.Valid() || r.hold.Recheck(ctx) != nil {
		return Manifest{}, ErrRecovery
	}
	_, err = r.service.Journal.SaveArchiveManifest(ctx, r.operation, m)
	if err != nil {
		return Manifest{}, ErrRecovery
	}
	return m, nil
}

func (r *removalRun) advancePreparation(ctx context.Context, stage Stage) error {
	var err error
	r.operation, err = r.service.Journal.AdvanceArchive(ctx, r.operation, stage)
	return err
}

func (r *removalRun) preparedCoverage(ctx context.Context) ([]Coverage, error) {
	coverage := make([]Coverage, 0, len(r.plan.Candidate.Databases))
	for _, database := range r.plan.Candidate.Databases {
		c, err := r.coverDatabase(ctx, database)
		if err != nil || !c.Admits(r.plan, database, r.operation.ID, r.service.now()) {
			return nil, ErrRecovery
		}
		coverage = append(coverage, c)
	}
	return coverage, nil
}

func (r *removalRun) coverDatabase(ctx context.Context, d Database) (Coverage, error) {
	for _, ref := range r.plan.Removal.Points {
		if ref.DatabaseID == d.Reference().DatabaseID {
			return r.service.Host.(RemovalHost).CoverPrepared(ctx, r.plan, ref, r.operation.ID, r.service.now())
		}
	}
	if d.Written() {
		return Coverage{}, ErrRefused
	}
	return r.service.Host.Cover(ctx, r.plan, d, r.operation.ID, r.service.now())
}

func removalExpiry(created time.Time, refs []PreparedRef) time.Time {
	expiry := created.Add(15 * time.Minute)
	for _, ref := range refs {
		if ref.ExpiresAt.Before(expiry) {
			expiry = ref.ExpiresAt
		}
	}
	return expiry
}
