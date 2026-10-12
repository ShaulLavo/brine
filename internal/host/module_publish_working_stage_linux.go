//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/store"
)

// publicationWorkingStage consumes a verified pristine restored root once.
// Native boot/publication may change that private root; the original source,
// claim, rooted directory identity and candidate remain protected throughout.
type publicationWorkingStage struct {
	source   *nativeRestoredSource
	attempt  *store.PublicationAttempt
	artifact *publicationArtifact
	scope    datapublish.ProofScope
}

func beginPublicationWorkingStage(ctx context.Context, source *nativeRestoredSource, attempt *store.PublicationAttempt, artifact *publicationArtifact) (*publicationWorkingStage, error) {
	if source == nil || source.unchanged(ctx) != nil || artifact == nil || artifact.preparation != source.preparation || artifact.unchanged(ctx) != nil {
		return nil, datapublish.ErrRecord
	}
	cursor, err := source.state.ReadPublicationAttemptCursor(ctx, attempt)
	if err != nil || cursor.Scope().PointID != source.pointID || cursor.Scope().BindingID != string(source.permit.Runtime.BindingID) || cursor.Scope().OldEpochID != string(source.permit.Runtime.EpochID) || cursor.Scope().CandidateWasmSHA256 != string(artifact.artifact.WasmSHA256()) || cursor.Scope().CandidateProgramWireHash != string(artifact.artifact.ProgramWireHash()) {
		return nil, datapublish.ErrRecord
	}
	if cursor.State() != datapublish.IntentRecorded || cursor.IntendedStep() != datapublish.RestoreStage {
		return nil, datapublish.ErrRecord
	}
	if _, err = source.state.RecordPublicationRestore(ctx, attempt, source.extracted, source.parent, source.evidence); err != nil {
		return nil, err
	}
	if err = preparePublicationWorkingCopy(ctx, source); err != nil {
		return nil, err
	}
	if err = source.state.BindPublicationAttemptWorkingRoot(ctx, attempt, source.root); err != nil {
		return nil, err
	}
	return &publicationWorkingStage{source: source, attempt: attempt, artifact: artifact, scope: cursor.Scope()}, nil
}

func (s *publicationWorkingStage) unchanged(ctx context.Context) error {
	if s == nil || s.source == nil || s.source.originalUnchanged(ctx) != nil || !s.source.preparation.Active(s.source.permit.Runtime.BindingID) || s.artifact == nil || s.artifact.unchanged(ctx) != nil || !publicationStageRootUnchanged(s.source.root, s.source.path, s.source.seal.root) {
		return datapublish.ErrRecord
	}
	cursor, err := s.source.state.ReadPublicationAttemptCursor(ctx, s.attempt)
	if err != nil || cursor.Scope() != s.scope {
		return datapublish.ErrRecord
	}
	return nil
}
