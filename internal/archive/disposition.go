package archive

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type DispositionStage string

const (
	CleanupIntent  DispositionStage = "cleanup_intent"
	RuntimeRetired DispositionStage = "runtime_retired"
	RenameIntent   DispositionStage = "rename_intent"
	Renamed        DispositionStage = "renamed"
	Retired        DispositionStage = "retired"
	AbandonIntent  DispositionStage = "abandon_intent"
	Abandoned      DispositionStage = "abandoned"
)

type Disposition struct {
	ArchiveID   data.ArchiveID   `json:"archive_id"`
	Sequence    uint64           `json:"sequence"`
	Stage       DispositionStage `json:"stage"`
	TreeIndex   int              `json:"tree_index"`
	CommittedAt time.Time        `json:"committed_at,omitempty"`
	RetainUntil time.Time        `json:"retain_until,omitempty"`
}

func DispositionNext(old, next Disposition, trees int) bool {
	if !data.ValidID(string(next.ArchiveID)) || next.Sequence != old.Sequence+1 || trees < 1 || trees > 16 || next.TreeIndex < 0 || next.TreeIndex >= trees || old.Sequence != 0 && old.ArchiveID != next.ArchiveID {
		return false
	}
	if next.Stage != Retired && next.Stage != Abandoned && (!next.CommittedAt.IsZero() || !next.RetainUntil.IsZero()) {
		return false
	}
	switch old.Stage {
	case "":
		return old.Sequence == 0 && next.TreeIndex == 0 && (next.Stage == CleanupIntent || next.Stage == AbandonIntent)
	case CleanupIntent:
		return (next.Stage == RuntimeRetired || next.Stage == AbandonIntent) && next.TreeIndex == 0
	case RuntimeRetired:
		return next.Stage == RenameIntent && next.TreeIndex == 0
	case RenameIntent:
		return next.Stage == Renamed && next.TreeIndex == old.TreeIndex
	case Renamed:
		if old.TreeIndex+1 < trees {
			return next.Stage == RenameIntent && next.TreeIndex == old.TreeIndex+1
		}
		return next.Stage == Retired && next.TreeIndex == old.TreeIndex && !next.CommittedAt.IsZero() && next.RetainUntil.Equal(next.CommittedAt.Add(30*24*time.Hour))
	case AbandonIntent:
		return next.Stage == Abandoned && next.TreeIndex == 0 && !next.CommittedAt.IsZero() && next.RetainUntil.IsZero()
	default:
		return false
	}
}

type DispositionJournal interface {
	LoadArchivePlanByArchive(context.Context, data.ArchiveID) (Plan, error)
	ReadArchiveDisposition(context.Context, data.ArchiveID) (Disposition, bool, error)
	AdvanceArchiveDisposition(context.Context, Operation, Disposition, Disposition) (Disposition, error)
	FinishArchiveDisposition(context.Context, Operation, Disposition, Disposition) (Disposition, error)
}
type DispositionHost interface {
	VerifyArchiveRuntime(context.Context, Plan) error
	RecheckArchive(context.Context, Plan, Operation, bool) error
	RetireArchiveRuntime(context.Context, Plan) error
	ArchiveRuntimeStopped(context.Context, Plan) (bool, error)
}

// Abandon restores ordinary managed startup permission; it never starts a writer
// or moves data. A committed app can then use the normal start plan/apply path.
func (s Service) Abandon(ctx context.Context, id data.ArchiveID) (Disposition, error) {
	return s.dispose(ctx, id, true)
}
func (s Service) Retire(ctx context.Context, id data.ArchiveID) (Disposition, error) {
	return s.dispose(ctx, id, false)
}
func (s Service) dispose(ctx context.Context, id data.ArchiveID, abandon bool) (out Disposition, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	defer func() {
		if err != nil && out.Sequence != 0 && out.Stage != Abandoned && out.Stage != Retired {
			err = ErrRecovery
		}
	}()
	if !data.ValidID(string(id)) {
		return out, ErrRefused
	}
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	journal, ok := s.Journal.(DispositionJournal)
	if !ok {
		return out, ErrRefused
	}
	host, ok := s.Host.(DispositionHost)
	if !ok {
		return out, ErrRefused
	}
	p, err := journal.LoadArchivePlanByArchive(ctx, id)
	if err != nil || !p.Valid() || p.ArchiveID != id || p.Requester != s.Requester {
		return out, ErrRefused
	}
	old, exists, err := journal.ReadArchiveDisposition(ctx, id)
	if err != nil {
		return out, err
	}
	if exists {
		if !abandon && old.Stage == Retired {
			return old, s.retireArchivedBackups(ctx, p)
		}
		if abandon && old.Stage == Abandoned {
			return old, nil
		}
		if !abandon || old.Stage != AbandonIntent && old.Stage != CleanupIntent {
			return old, ErrRecovery
		}
		out = old
	}
	op, exists, err := s.Journal.ReadArchiveOperation(ctx, p.ID)
	stage, validStage := StageAt(op.Sequence)
	if err != nil || !exists || !validStage || stage != op.Stage || op.PlanID != p.ID || op.ArchiveID != id {
		return out, ErrRecovery
	}
	var m Manifest
	if !abandon {
		m, err = s.Journal.ReadArchiveManifest(ctx, id)
		if err != nil || !m.Valid() || op.Stage != Prepared || op.ID != m.OperationID || m.Plan.ID != p.ID {
			return out, ErrRecovery
		}
	}
	// Affirmative pristine artifact ownership is required before cleanup intent,
	// and before adopting any interrupted pre-effect cleanup as abandonment.
	if host.VerifyArchiveRuntime(ctx, p) != nil {
		return out, ErrRefused
	}
	if abandon {
		if host.RecheckArchive(ctx, p, op, true) != nil {
			return out, ErrRefused
		}
		if out.Stage != AbandonIntent {
			next := Disposition{ArchiveID: id, Sequence: out.Sequence + 1, Stage: AbandonIntent}
			out, err = journal.AdvanceArchiveDisposition(ctx, op, out, next)
			if err != nil {
				return out, ErrRecovery
			}
		}
		// Stop only owned managed services; the durable intent precedes every effect.
		if s.Host.StopWriters(ctx, p) != nil || s.Host.StopReplicas(ctx, p) != nil {
			return out, ErrRecovery
		}
	}
	if !abandon {
		for i, c := range m.Coverage {
			if !c.Admits(p, p.Candidate.Databases[i], op.ID, s.now()) {
				return out, ErrRefused
			}
		}
	}
	if err = host.RecheckArchive(ctx, p, op, abandon); err != nil {
		return out, ErrRefused
	}
	stopped, err := s.Host.WritersStopped(ctx, p)
	if err != nil || !stopped {
		return out, ErrRefused
	}
	stopped, err = s.Host.ReplicasStopped(ctx, p)
	if err != nil || !stopped {
		return out, ErrRefused
	}
	unlock, err := s.Host.AcquireLifetimeLocks(ctx, p)
	if err != nil {
		return out, ErrRefused
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err = host.RecheckArchive(ctx, p, op, abandon); err != nil {
		return out, ErrRefused
	}
	stopped, err = s.Host.WritersStopped(ctx, p)
	if err != nil || !stopped {
		return out, ErrRefused
	}
	for _, tree := range p.Trees {
		if err = data.VerifyUnarchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID); err != nil {
			return out, ErrRefused
		}
	}
	for _, d := range p.Candidate.Databases {
		if verifyDatabaseAllocation(d, p.ArchiveID, false) != nil {
			return out, ErrRefused
		}
	}
	advance := func(stage DispositionStage, index int, finish bool) error {
		next := Disposition{ArchiveID: id, Sequence: out.Sequence + 1, Stage: stage, TreeIndex: index}
		if finish {
			next.CommittedAt = s.now()
			if stage == Retired {
				next.RetainUntil = next.CommittedAt.Add(30 * 24 * time.Hour)
			}
			out, err = journal.FinishArchiveDisposition(ctx, op, out, next)
		} else {
			out, err = journal.AdvanceArchiveDisposition(ctx, op, out, next)
		}
		return err
	}
	if abandon {
		if host.VerifyArchiveRuntime(ctx, p) != nil || host.RecheckArchive(ctx, p, op, abandon) != nil {
			return out, ErrRecovery
		}
		for _, tree := range p.Trees {
			if data.VerifyUnarchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID) != nil {
				return out, ErrRecovery
			}
		}
		stopped, err = s.Host.WritersStopped(ctx, p)
		if err != nil || !stopped {
			return out, ErrRecovery
		}
		stopped, err = s.Host.ReplicasStopped(ctx, p)
		if err != nil || !stopped || ctx.Err() != nil {
			return out, ErrRecovery
		}
		if advance(Abandoned, 0, true) != nil {
			return out, ErrRecovery
		}
		return out, nil
	}
	if advance(CleanupIntent, 0, false) != nil || host.RetireArchiveRuntime(ctx, p) != nil || advance(RuntimeRetired, 0, false) != nil {
		return out, ErrRecovery
	}
	for i, tree := range p.Trees {
		if advance(RenameIntent, i, false) != nil {
			return out, ErrRecovery
		}
		stopped, err = host.ArchiveRuntimeStopped(ctx, p)
		if err != nil || !stopped || ctx.Err() != nil {
			return out, ErrRecovery
		}
		for j, c := range m.Coverage {
			if !c.Admits(p, p.Candidate.Databases[j], op.ID, s.now()) {
				return out, ErrRecovery
			}
		}
		for _, d := range p.Candidate.Databases {
			for j, epoch := range d.ArchiveBindings() {
				if !d.Retention()[j].Admits(epoch.Destination, s.now()) {
					return out, ErrRecovery
				}
			}
			if d.Reference().Root == tree.Root && verifyDatabaseAllocation(d, p.ArchiveID, false) != nil {
				return out, ErrRecovery
			}
		}
		if err = data.RenameArchiveTree(tree, p.Candidate.Incarnation, p.ArchiveID); err != nil {
			return out, ErrRecovery
		}
		if advance(Renamed, i, false) != nil {
			return out, ErrRecovery
		}
	}
	for _, tree := range p.Trees {
		if data.VerifyArchivedTree(tree, p.Candidate.Incarnation, p.ArchiveID) != nil {
			return out, ErrRecovery
		}
	}
	for _, d := range p.Candidate.Databases {
		if verifyDatabaseAllocation(d, p.ArchiveID, true) != nil {
			return out, ErrRecovery
		}
	}
	if mover, ok := s.Host.(interface {
		ArchiveNativeTrees(context.Context, Plan) error
	}); ok {
		if mover.ArchiveNativeTrees(ctx, p) != nil {
			return out, ErrRecovery
		}
	}
	if advance(Retired, len(p.Trees)-1, true) != nil {
		return out, ErrRecovery
	}
	return out, nil
}
