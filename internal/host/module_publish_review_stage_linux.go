//go:build linux

package host

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationReviewWork struct {
	source      *nativeRestoredSource
	extraction  extractedPublicationCandidate
	preparation *store.PublicationReviewPreparation
}

type publicationReviewPreflight struct {
	work        *publicationReviewWork
	lease       *publicationStageLease
	observation moduleStageObservation
	parsed      spacetimedb.NativePreflightDescription
}

func beginPublicationReviewWork(ctx context.Context, source *nativeRestoredSource, extraction extractedPublicationCandidate) (*publicationReviewWork, error) {
	if source == nil || source.unchanged(ctx) != nil || source.permit.Runtime.State != data.EngineInitialized || extraction.unchanged(ctx) != nil || extraction.lease.artifact.preparation != source.preparation {
		return nil, datapublish.ErrRecord
	}
	preparation, err := source.state.BeginPublicationReview(ctx, source.preparation, source.pointID, extraction.lease.artifact.artifact)
	if err != nil {
		return nil, err
	}
	return &publicationReviewWork{source: source, extraction: extraction, preparation: preparation}, nil
}

func (w *publicationReviewWork) unchanged(ctx context.Context) error {
	if w == nil || w.source == nil || w.source.originalUnchanged(ctx) != nil || w.extraction.unchanged(ctx) != nil || !publicationStageRootUnchanged(w.source.root, w.source.path, w.source.seal.root) {
		return datapublish.ErrRecord
	}
	return w.source.state.CheckPublicationReviewPreparation(ctx, w.preparation)
}

func acquirePublicationReviewStage(ctx context.Context, work *publicationReviewWork, images *podman.Client) (*publicationStageLease, error) {
	if work.unchanged(ctx) != nil {
		return nil, datapublish.ErrRecord
	}
	lease, err := acquireRestoredStage(ctx, work.source, work.extraction.lease.artifact, images)
	if err != nil {
		return nil, err
	}
	lease.review = work
	if lease.sourceUnchanged(ctx) != nil {
		lease.material.clear()
		return nil, datapublish.ErrRecord
	}
	return lease, nil
}

func runPublicationReviewPreflight(ctx context.Context, lease *publicationStageLease, runner localexec.InputRunner, session localexec.Session) (publicationReviewPreflight, error) {
	if lease == nil || lease.review == nil || lease.working != nil || lease.review.unchanged(ctx) != nil || lease.attempted {
		return publicationReviewPreflight{}, datapublish.ErrRecord
	}
	candidate := lease.review.extraction.candidate
	request := moduleStageInput{Action: moduleStagePreflight, Runtime: lease.permit.Runtime.Clone(), Token: string(lease.material.publisher), WasmSHA256: candidate.WasmSHA256(), ProgramWireHash: candidate.ProgramWireHash(), SchemaSHA256: candidate.Schema().SHA256()}
	observed, err := runPublicationStage(ctx, lease, request, runner, session)
	if err != nil || len(observed.Preflight) == 0 || !moduleStageResultMatches(observed, request, observed.Preflight) || lease.review.unchanged(ctx) != nil {
		return publicationReviewPreflight{}, datapublish.ErrRecord
	}
	parsed, err := spacetimedb.DescribeNativePreflight(observed.Preflight)
	if err != nil || !parsed.Automatic() || parsed.MajorUpgrade() || len(parsed.TokenBytes()) == 0 {
		return publicationReviewPreflight{}, datapublish.ErrRecord
	}
	return publicationReviewPreflight{work: lease.review, lease: lease, observation: observed, parsed: parsed}, nil
}

func (p publicationReviewPreflight) unchanged(ctx context.Context) error {
	if p.work == nil || p.lease == nil || p.lease.review != p.work || p.work.unchanged(ctx) != nil || !p.lease.attempted || !p.lease.disposed || p.lease.process != nil || p.lease.containerID != "" {
		return datapublish.ErrRecord
	}
	parsed, err := spacetimedb.DescribeNativePreflight(p.observation.Preflight)
	if err != nil || !parsed.Automatic() || parsed.MajorUpgrade() || parsed.ResponseSHA256() != p.parsed.ResponseSHA256() || parsed.TokenSHA256() != p.parsed.TokenSHA256() || !slices.Equal(parsed.TokenBytes(), p.parsed.TokenBytes()) {
		return datapublish.ErrRecord
	}
	return checkModuleStagePublisherProof(p.observation, p.lease.startedAt, p.lease.finishedAt)
}
