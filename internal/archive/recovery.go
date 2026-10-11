package archive

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type RuntimeInspection struct {
	Complete bool `json:"complete"`
}
type TreeInspection struct {
	Index    int    `json:"index"`
	Location string `json:"location"`
}
type Inspection struct {
	Disposition Disposition       `json:"disposition"`
	Runtime     RuntimeInspection `json:"runtime"`
	Trees       []TreeInspection  `json:"trees"`
	// CanReconcile describes journal/artifact structure only. Reconcile must
	// independently recheck current ownership, quiescence and remote coverage.
	CanReconcile bool `json:"can_reconcile"`
}

type RecoveryHost interface {
	InspectArchiveRuntime(context.Context, Plan) (RuntimeInspection, error)
	ReconcileArchiveRuntime(context.Context, Plan) error
	// CheckArchiveRecovery verifies retained ownership, held fences, both path
	// locations, current schema/retention and freshly restored original coverage.
	// It must independently prove writer and replicator quiescence without stops.
	CheckArchiveRecovery(context.Context, Plan, Operation, Manifest) error
}

func inspectTrees(p Plan, d Disposition) ([]TreeInspection, bool) {
	trees := make([]TreeInspection, len(p.Trees))
	valid := d.ArchiveID == p.ArchiveID && d.Sequence > 0 && d.TreeIndex >= 0 && d.TreeIndex < len(p.Trees)
	for i, t := range p.Trees {
		location := "unknown"
		if data.VerifyUnarchivedTree(t, p.Candidate.Incarnation, p.ArchiveID) == nil {
			location = "source"
		} else if data.VerifyArchivedTree(t, p.Candidate.Incarnation, p.ArchiveID) == nil {
			location = "archive"
		}
		trees[i] = TreeInspection{Index: i, Location: location}
		expected := "source"
		if d.Stage == RenameIntent && i < d.TreeIndex || d.Stage == Renamed && i <= d.TreeIndex {
			expected = "archive"
		}
		if location == "unknown" || location != expected && !(d.Stage == RenameIntent && i == d.TreeIndex && location == "archive") {
			valid = false
		}
	}
	return trees, valid
}

// Inspect changes no journal or runtime artifact. Terminal receipts are historical
// and do not inspect a subsequently recreated incarnation.
func (s Service) Inspect(ctx context.Context, id data.ArchiveID) (out Inspection, err error) {
	if !data.ValidID(string(id)) {
		return out, ErrRefused
	}
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	_, _, out, err = s.inspectRecovery(ctx, id)
	return out, err
}
func (s Service) inspectRecovery(ctx context.Context, id data.ArchiveID) (p Plan, o Operation, out Inspection, err error) {
	journal, ok := s.Journal.(DispositionJournal)
	if !ok {
		return p, o, out, ErrRefused
	}
	p, err = journal.LoadArchivePlanByArchive(ctx, id)
	if err != nil || !p.Valid() || p.ArchiveID != id || p.Requester != s.Requester {
		return p, o, out, ErrRefused
	}
	d, exists, err := journal.ReadArchiveDisposition(ctx, id)
	if err != nil || !exists {
		return p, o, out, ErrRecovery
	}
	out.Disposition = d
	if d.Stage == Retired || d.Stage == Abandoned {
		return p, o, out, nil
	}
	o, exists, err = s.Journal.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !exists || o.PlanID != p.ID || o.ArchiveID != id || o.Stage != Prepared || o.Sequence != 8 {
		return p, o, out, ErrRecovery
	}
	host, ok := s.Host.(RecoveryHost)
	if !ok {
		return p, o, out, ErrRefused
	}
	out.Runtime, err = host.InspectArchiveRuntime(ctx, p)
	if err != nil {
		return p, o, out, ErrRecovery
	}
	var valid bool
	out.Trees, valid = inspectTrees(p, d)
	out.CanReconcile = valid && (d.Stage == CleanupIntent || d.Stage == RuntimeRetired || d.Stage == RenameIntent || d.Stage == Renamed) && (d.Stage == CleanupIntent || out.Runtime.Complete)
	return p, o, out, nil
}

// Reconcile adopts only positively inspected effects. A rename intent with its
// recorded inode still at source authorizes one no-replace movement; that inode
// already at archive authorizes only directory sync and the missing receipt.
// Fences remain held, including after retirement. No service is started or stopped.
func (s Service) Reconcile(ctx context.Context, id data.ArchiveID) (out Disposition, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if !data.ValidID(string(id)) {
		return out, ErrRefused
	}
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	p, o, inspection, err := s.inspectRecovery(ctx, id)
	out = inspection.Disposition
	if err != nil {
		return out, err
	}
	if out.Stage == Retired || out.Stage == Abandoned {
		return out, nil
	}
	if !inspection.CanReconcile {
		return out, ErrRecovery
	}
	host := s.Host.(RecoveryHost)
	journal := s.Journal.(DispositionJournal)
	m, err := s.Journal.ReadArchiveManifest(ctx, id)
	if err != nil || !m.Valid() || m.OperationID != o.ID || m.Plan.ID != p.ID {
		return out, ErrRecovery
	}
	unlock, err := s.Host.AcquireLifetimeLocks(ctx, p)
	if err != nil {
		return out, ErrRecovery
	}
	defer func() { err = errors.Join(err, unlock()) }()
	check := func() error { return host.CheckArchiveRecovery(ctx, p, o, m) }
	if check() != nil {
		return out, ErrRecovery
	}
	if host.ReconcileArchiveRuntime(ctx, p) != nil {
		return out, ErrRecovery
	}
	advance := func(stage DispositionStage, index int) error {
		next := Disposition{ArchiveID: id, Sequence: out.Sequence + 1, Stage: stage, TreeIndex: index}
		if stage == Retired {
			next.CommittedAt = s.now()
			next.RetainUntil = next.CommittedAt.Add(30 * 24 * time.Hour)
			out, err = journal.FinishArchiveDisposition(ctx, o, out, next)
		} else {
			out, err = journal.AdvanceArchiveDisposition(ctx, o, out, next)
		}
		return err
	}
	for out.Stage != Retired {
		if ctx.Err() != nil || check() != nil {
			return out, ErrRecovery
		}
		trees, valid := inspectTrees(p, out)
		if !valid {
			return out, ErrRecovery
		}
		switch out.Stage {
		case CleanupIntent:
			err = advance(RuntimeRetired, 0)
		case RuntimeRetired:
			err = advance(RenameIntent, 0)
		case RenameIntent:
			tree := p.Trees[out.TreeIndex]
			if trees[out.TreeIndex].Location == "source" {
				err = data.RenameArchiveTree(tree, p.Candidate.Incarnation, id)
			} else {
				err = data.SyncArchivedTree(tree, p.Candidate.Incarnation, id)
			}
			if err == nil {
				err = advance(Renamed, out.TreeIndex)
			}
		case Renamed:
			if out.TreeIndex+1 < len(p.Trees) {
				err = advance(RenameIntent, out.TreeIndex+1)
			} else {
				err = advance(Retired, out.TreeIndex)
			}
		default:
			return out, ErrRecovery
		}
		if err != nil {
			return out, ErrRecovery
		}
	}
	return out, nil
}
