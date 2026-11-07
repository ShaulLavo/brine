package archive

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

func (s Service) removalDispositionPlan(ctx context.Context, id data.ArchiveID) (Plan, bool, error) {
	j, ok := s.Journal.(DispositionJournal)
	if !ok {
		return Plan{}, false, ErrRefused
	}
	p, err := j.LoadArchivePlanByArchive(ctx, id)
	return p, err == nil && p.Removal != nil, err
}

func (s Service) disposeRemoval(ctx context.Context, p Plan, abandon, recovering bool) (out Disposition, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err = s.authorizeRemoval(ctx, p); err != nil {
		return out, err
	}
	j, ok := s.Journal.(DispositionJournal)
	h, hostOK := s.Host.(DispositionHost)
	if !ok || !hostOK {
		return out, ErrRefused
	}
	out, _, err = j.ReadArchiveDisposition(ctx, p.ArchiveID)
	if err != nil {
		return out, err
	}
	if out.Stage == Retired || out.Stage == Abandoned {
		return out, nil
	}
	hold, err := s.holdRemoval(ctx, p)
	if err != nil {
		return out, errors.Join(ErrRecovery, err)
	}
	defer func() { err = errors.Join(err, hold.Close()) }()
	r := removalRun{service: s, plan: p, disposition: out, hold: hold, action: removalDispositionAction(abandon, recovering), journal: j, host: h}
	r.operation, ok, err = s.Journal.ReadArchiveOperation(ctx, p.ID)
	stage, valid := StageAt(r.operation.Sequence)
	if err != nil || !ok || !valid || stage != r.operation.Stage || r.operation.PlanID != p.ID || r.operation.ArchiveID != p.ArchiveID {
		return out, ErrRecovery
	}
	if err = r.admitTask(ctx); err != nil {
		return out, errors.Join(ErrRefused, err)
	}
	if abandon {
		err = r.abandon(ctx)
		return r.disposition, err
	}
	r.manifest, err = s.Journal.ReadArchiveManifest(ctx, p.ArchiveID)
	if err != nil || !r.manifest.Valid() || r.operation.Stage != Prepared || r.manifest.OperationID != r.operation.ID || r.manifest.Plan.ID != p.ID {
		return out, ErrRecovery
	}
	if !recovering && out.Sequence != 0 {
		return out, ErrRecovery
	}
	if err = r.admitForward(ctx, recovering); err != nil {
		return out, err
	}
	err = r.retire(ctx, recovering)
	return r.disposition, err
}

func (r *removalRun) abandon(ctx context.Context) error {
	if r.disposition.Stage != "" && r.disposition.Stage != CleanupIntent && r.disposition.Stage != AbandonIntent {
		return ErrRecovery
	}
	if r.host.VerifyArchiveRuntime(ctx, r.plan) != nil || r.host.RecheckArchive(ctx, r.plan, r.operation, true) != nil || removalStopped(ctx, r.service.Host, r.plan) != nil || verifyRemovalSources(r.plan, false) != nil || r.hold.Recheck(ctx) != nil {
		return ErrRefused
	}
	if r.disposition.Stage != AbandonIntent {
		if r.advanceDisposition(ctx, AbandonIntent, 0) != nil {
			return ErrRecovery
		}
	}
	if r.host.VerifyArchiveRuntime(ctx, r.plan) != nil || r.host.RecheckArchive(ctx, r.plan, r.operation, true) != nil || removalStopped(ctx, r.service.Host, r.plan) != nil || verifyRemovalSources(r.plan, false) != nil || r.hold.Recheck(ctx) != nil {
		return ErrRecovery
	}
	return r.advanceDisposition(ctx, Abandoned, 0)
}

func (r *removalRun) admitForward(ctx context.Context, recovering bool) error {
	if !removalLayoutValid(r.plan, r.disposition) {
		return ErrRecovery
	}
	if recovering {
		h, ok := r.service.Host.(RemovalRecoveryHost)
		if !ok {
			return ErrRefused
		}
		receipts, err := h.CheckRemovalRecovery(ctx, r.plan, r.operation, r.manifest, r.service.Operation)
		if err != nil {
			return ErrRecovery
		}
		if err = h.RecordRemovalRecovery(ctx, r.service.Operation, r.operation, receipts, r.hold); err != nil {
			return ErrRecovery
		}
		return nil
	}
	if r.host.VerifyArchiveRuntime(ctx, r.plan) != nil || r.host.RecheckArchive(ctx, r.plan, r.operation, false) != nil || removalStopped(ctx, r.service.Host, r.plan) != nil || r.hold.Recheck(ctx) != nil || verifyRemovalSources(r.plan, false) != nil || !r.directCoverage() {
		return ErrRefused
	}
	return nil
}

func (r *removalRun) directCoverage() bool {
	for i, c := range r.manifest.Coverage {
		if !c.Admits(r.plan, r.plan.Candidate.Databases[i], r.operation.ID, r.service.now()) {
			return false
		}
	}
	return preparationRetentionAdmits(r.plan, r.service.now())
}

func (r *removalRun) retire(ctx context.Context, recovering bool) error {
	if r.disposition.Sequence == 0 {
		if r.advanceDisposition(ctx, CleanupIntent, 0) != nil {
			return ErrRecovery
		}
	}
	if err := r.admitTask(ctx); err != nil {
		return ErrRecovery
	}
	if recovering && r.forwardCheck(ctx, recovering) != nil {
		return ErrRecovery
	}
	if recovering {
		h, ok := r.service.Host.(RecoveryHost)
		if !ok || h.ReconcileArchiveRuntime(ctx, r.plan) != nil {
			return ErrRecovery
		}
	} else if r.host.RetireArchiveRuntime(ctx, r.plan) != nil {
		return ErrRecovery
	}
	for r.disposition.Stage != Retired {
		if err := r.forwardCheck(ctx, recovering); err != nil {
			return ErrRecovery
		}
		if err := r.retirementStep(ctx); err != nil {
			return ErrRecovery
		}
	}
	return nil
}

func (r *removalRun) forwardCheck(ctx context.Context, recovering bool) error {
	if err := r.admitTask(ctx); err != nil {
		return err
	}
	if !removalLayoutValid(r.plan, r.disposition) {
		return ErrRecovery
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if recovering {
		h := r.service.Host.(RemovalRecoveryHost)
		_, err := h.CheckRemovalRecovery(ctx, r.plan, r.operation, r.manifest, r.service.Operation)
		return err
	}
	if !r.directCoverage() {
		return ErrRecovery
	}
	stopped, err := r.host.ArchiveRuntimeStopped(ctx, r.plan)
	if err != nil || !stopped {
		return ErrRecovery
	}
	return nil
}

func (r *removalRun) retirementStep(ctx context.Context) error {
	switch r.disposition.Stage {
	case CleanupIntent:
		return r.advanceDisposition(ctx, RuntimeRetired, 0)
	case RuntimeRetired:
		return r.advanceDisposition(ctx, RenameIntent, 0)
	case RenameIntent:
		return r.rename(ctx)
	case Renamed:
		if r.disposition.TreeIndex+1 < len(r.plan.Trees) {
			return r.advanceDisposition(ctx, RenameIntent, r.disposition.TreeIndex+1)
		}
		if verifyRemovalSources(r.plan, true) != nil {
			return ErrRecovery
		}
		return r.advanceDisposition(ctx, Retired, r.disposition.TreeIndex)
	default:
		return ErrRecovery
	}
}

func (r *removalRun) rename(ctx context.Context) error {
	trees, valid := inspectTrees(r.plan, r.disposition)
	if !valid {
		return ErrRecovery
	}
	index := r.disposition.TreeIndex
	tree := r.plan.Trees[index]
	if trees[index].Location == "archive" {
		if data.SyncArchivedTree(tree, r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
			return ErrRecovery
		}
		return r.advanceDisposition(ctx, Renamed, index)
	}
	if index == 0 && r.hold.Recheck(ctx) != nil {
		return ErrRecovery
	}
	for _, d := range r.plan.Candidate.Databases {
		if d.Reference().Root == tree.Root && verifyDatabaseAllocation(d, r.plan.ArchiveID, false) != nil {
			return ErrRecovery
		}
	}
	if data.RenameArchiveTree(tree, r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		return ErrRecovery
	}
	return r.advanceDisposition(ctx, Renamed, index)
}

func (r *removalRun) advanceDisposition(ctx context.Context, stage DispositionStage, index int) error {
	if err := r.admitTask(ctx); err != nil {
		return err
	}
	next := Disposition{ArchiveID: r.plan.ArchiveID, Sequence: r.disposition.Sequence + 1, Stage: stage, TreeIndex: index}
	var err error
	if stage != Retired && stage != Abandoned {
		r.disposition, err = r.journal.AdvanceArchiveDisposition(ctx, r.operation, r.disposition, next)
		return err
	}
	next.CommittedAt = r.service.now()
	if stage == Retired {
		next.RetainUntil = next.CommittedAt.Add(30 * 24 * time.Hour)
	}
	var receipt RemovalReceipt
	r.disposition, receipt, err = r.service.Journal.(RemovalJournal).FinishRemoval(ctx, r.service.Operation, r.operation, r.disposition, next, r.hold)
	if err != nil {
		return err
	}
	if !receipt.Valid() || receipt.OperationID != r.service.Operation || receipt.ArchiveID != r.plan.ArchiveID || receipt.PlanID != r.plan.ID || receipt.Outcome != string(stage) {
		return ErrRecovery
	}
	return nil
}

func removalLayoutValid(p Plan, d Disposition) bool {
	if d.Sequence == 0 {
		d = Disposition{ArchiveID: p.ArchiveID, Sequence: 1, Stage: CleanupIntent}
	}
	_, valid := inspectTrees(p, d)
	return valid
}

func removalDispositionAction(abandon, recovering bool) RemovalAction {
	if abandon {
		return RemovalAbandon
	}
	if recovering {
		return RemovalContinue
	}
	return RemovalDirectRetire
}

func (r *removalRun) admitTask(ctx context.Context) error {
	return r.service.Journal.(RemovalJournal).AdmitRemovalDisposition(ctx, r.service.Operation, r.operation, r.action, r.hold)
}
