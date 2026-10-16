//go:build linux

package host

import (
	"context"
	"errors"
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

func beginPublicationReviewWork(ctx context.Context, source *nativeRestoredSource, preparation *store.PublicationReviewPreparation, extraction extractedPublicationCandidate) (*publicationReviewWork, error) {
	if source == nil || source.unchanged(ctx) != nil || preparation == nil || source.state.CheckPublicationReviewPreparation(ctx, preparation) != nil || preparation.PointID() != source.pointID || extraction.unchanged(ctx) != nil || extraction.lease.artifact.preparation != source.preparation || preparation.Artifact().WasmSHA256() != extraction.lease.artifact.artifact.WasmSHA256() {
		return nil, datapublish.ErrRecord
	}
	if err := source.state.BindPublicationReviewRestore(ctx, preparation, source.extracted, source.parent, source.evidence); err != nil {
		return nil, err
	}
	if source.original != nil {
		bundle, err := source.state.ReadNativePreparedBundle(ctx, source.original.stopped)
		if err != nil {
			return nil, err
		}
		diff, err := spacetimedb.CompareSchemas(bundle.Schema(), extraction.candidate.Schema())
		if err != nil {
			return nil, err
		}
		if diff.Decision() != spacetimedb.StagedValidationRequired {
			return nil, errors.Join(datapublish.ErrRecord, datapublish.ErrOperatorProcedure)
		}
	}
	if err := preparePublicationWorkingCopy(ctx, source); err != nil {
		return nil, err
	}
	if err := source.state.BindPublicationReviewWorkingRoot(ctx, preparation, source.root, source.path); err != nil {
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
	if work.unchanged(ctx) != nil || work.source.permit.Runtime.State != data.EngineInitialized {
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

func completeInitialPublicationReview(ctx context.Context, work *publicationReviewWork) (datapublish.Plan, error) {
	if work.unchanged(ctx) != nil || work.source.permit.Runtime.State != data.EngineUnpublished || work.source.empty == nil || !data.ValidCatalogHash(work.source.initialPublisher) || !data.ValidCatalogHash(work.source.publisherProofSHA) {
		return datapublish.Plan{}, datapublish.ErrRecord
	}
	owner, err := work.source.state.ReadNativeInitialOwner(ctx, work.source.empty.stopped)
	if err != nil || owner.Publisher != work.source.initialPublisher || owner.PublisherProofSHA256 != work.source.publisherProofSHA {
		return datapublish.Plan{}, datapublish.ErrRecord
	}
	extraction, err := work.extraction.provenance(ctx)
	if err != nil {
		return datapublish.Plan{}, err
	}
	observed := store.PublicationReviewObservation{Descriptor: slices.Clone(work.extraction.descriptor), Extraction: extraction, Runtime: work.source.permit.Runtime.Clone(), Publisher: owner.Publisher, PublisherProof: slices.Clone(owner.PublisherProof)}
	return work.source.state.CompletePublicationReview(ctx, work.preparation, observed)
}

func (p publicationReviewPreflight) complete(ctx context.Context) (datapublish.Plan, error) {
	observed, err := p.observationRecord(ctx)
	if err != nil {
		return datapublish.Plan{}, err
	}
	return p.work.source.state.CompletePublicationReview(ctx, p.work.preparation, observed)
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

func (p publicationReviewPreflight) observationRecord(ctx context.Context) (store.PublicationReviewObservation, error) {
	if p.unchanged(ctx) != nil {
		return store.PublicationReviewObservation{}, datapublish.ErrRecord
	}
	extraction, err := p.work.extraction.provenance(ctx)
	if err != nil {
		return store.PublicationReviewObservation{}, err
	}
	lease, child := p.lease, p.lease.observation
	return store.PublicationReviewObservation{Descriptor: slices.Clone(p.work.extraction.descriptor), Extraction: extraction, Preflight: slices.Clone(p.observation.Preflight), PublisherProof: slices.Clone(p.observation.PublisherProof),
		Runtime: lease.permit.Runtime.Clone(), Publisher: child.Publisher, HelperPID: lease.helperPID, HelperStartTicks: lease.helperTicks, HelperCgroup: lease.helperCgroup,
		ChildPID: child.ChildPID, ChildStartTicks: child.ChildStartTicks, StartedAt: lease.startedAt, FinishedAt: lease.finishedAt, Disposed: lease.disposed && child.ChildBound && child.ChildStopped}, nil
}
