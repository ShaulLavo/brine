package archive

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
)

type PreparationInspection struct {
	Stage        Stage  `json:"stage"`
	Sequence     uint64 `json:"sequence"`
	Manifest     bool   `json:"manifest"`
	CanAbandon   bool   `json:"can_abandon"`
	ClosedReason string `json:"closed_reason,omitempty"`
}

func (s Service) inspectRemoval(ctx context.Context, p Plan) (out Inspection, err error) {
	release, err := s.guard(ctx)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, release()) }()
	j, ok := s.Journal.(DispositionJournal)
	if !ok || !p.Valid() || p.Requester != s.Requester {
		return out, ErrRefused
	}
	d, exists, err := j.ReadArchiveDisposition(ctx, p.ArchiveID)
	if err != nil {
		return out, err
	}
	if !exists {
		d.ArchiveID = p.ArchiveID
	}
	out.Disposition = d
	if exists && (d.Stage == Retired || d.Stage == Abandoned) {
		return out, nil
	}
	o, exists, err := s.Journal.ReadArchiveOperation(ctx, p.ID)
	stage, valid := StageAt(o.Sequence)
	if err != nil || !exists || !valid || stage != o.Stage || o.PlanID != p.ID || o.ArchiveID != p.ArchiveID {
		return out, ErrRecovery
	}
	out.Preparation = &PreparationInspection{Stage: o.Stage, Sequence: o.Sequence}
	if o.Stage == Prepared {
		m, err := s.Journal.ReadArchiveManifest(ctx, p.ArchiveID)
		if err != nil || !m.Valid() || m.OperationID != o.ID || m.Plan.ID != p.ID {
			out.Preparation.ClosedReason = "manifest_unavailable"
			return out, nil
		}
		out.Preparation.Manifest = true
	}
	h, ok := s.Host.(RecoveryHost)
	if !ok {
		return out, ErrRefused
	}
	out.Runtime, err = h.InspectArchiveRuntime(ctx, p)
	if err != nil {
		out.Preparation.ClosedReason = "runtime_unknown"
		return out, nil
	}
	layout := d
	if layout.Sequence == 0 {
		layout = Disposition{ArchiveID: p.ArchiveID, Sequence: 1, Stage: CleanupIntent}
	}
	out.Trees, valid = inspectTrees(p, layout)
	if !valid {
		out.Preparation.ClosedReason = "tree_conflict"
		return out, nil
	}
	pristine := d.Stage == "" || d.Stage == CleanupIntent || d.Stage == AbandonIntent
	dh, ok := s.Host.(DispositionHost)
	out.Preparation.CanAbandon = pristine && ok && dh.VerifyArchiveRuntime(ctx, p) == nil
	out.CanReconcile = out.Preparation.Manifest && (d.Stage == "" || d.Stage == CleanupIntent || out.Runtime.Complete && (d.Stage == RuntimeRetired || d.Stage == RenameIntent || d.Stage == Renamed))
	if !out.Preparation.Manifest {
		out.Preparation.ClosedReason = "continue_unavailable"
	}
	return out, nil
}

func (s Service) removalForArchive(ctx context.Context, id data.ArchiveID) (Plan, bool) {
	p, removal, _ := s.removalDispositionPlan(ctx, id)
	return p, removal
}
