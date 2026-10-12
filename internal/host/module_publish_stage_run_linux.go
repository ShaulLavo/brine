//go:build linux

package host

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationStageEvidence struct {
	lease       *publicationStageLease
	source      *nativeRestoredSource
	scope       datapublish.ProofScope
	observation moduleStageObservation
	stageRoot   engineTreeMember
}

func runRestoredPublicationStage(ctx context.Context, lease *publicationStageLease, state *store.Store, attempt *store.PublicationAttempt, action moduleStageAction, runner localexec.InputRunner, session localexec.Session) (publicationStageEvidence, error) {
	if lease == nil || lease.restored == nil || lease.restored.state != state || lease.working == nil || lease.working.attempt != attempt || lease.working.unchanged(ctx) != nil || lease.attempted {
		return publicationStageEvidence{}, datapublish.ErrRecord
	}
	review, err := state.ReadPublicationAttemptReview(ctx, attempt)
	if err != nil {
		return publicationStageEvidence{}, datapublish.ErrRecord
	}
	cursor, err := state.ReadPublicationAttemptCursor(ctx, attempt)
	if err != nil || cursor.State() != datapublish.IntentRecorded || cursor.Scope().PointID != lease.restored.pointID || cursor.Scope().PlanID != review.Plan().ID() {
		return publicationStageEvidence{}, datapublish.ErrRecord
	}
	step, err := moduleStageCursorStep(action)
	if err != nil {
		return publicationStageEvidence{}, err
	}
	if cursor.IntendedStep() != step {
		return publicationStageEvidence{}, datapublish.ErrRecord
	}
	artifact, plan := lease.artifact.artifact, review.Plan()
	if artifact.WasmSHA256() != review.Artifact().WasmSHA256() || artifact.ProgramWireHash() != review.Artifact().ProgramWireHash() {
		return publicationStageEvidence{}, datapublish.ErrRecord
	}
	request := moduleStageInput{Action: action, Runtime: lease.permit.Runtime.Clone(), Token: string(lease.material.publisher), WasmSHA256: string(artifact.WasmSHA256()), ProgramWireHash: string(artifact.ProgramWireHash()), SchemaSHA256: plan.Candidate().Schema().SHA256()}
	if action == moduleStageCreate {
		if plan.Source().Kind() != datapublish.CreateSource || !data.ValidCatalogHash(lease.restored.initialPublisher) || !data.ValidCatalogHash(lease.restored.publisherProofSHA) {
			return publicationStageEvidence{}, datapublish.ErrRecord
		}
		request.CreationName, request.ExpectedPublisher = plan.CreationName(), lease.restored.initialPublisher
	}
	if action == moduleStagePublish {
		request.PreflightSHA256, request.PreflightTokenSHA = plan.Preflight().ResponseSHA256, plan.Preflight().TokenSHA256
	}
	observation, err := runPublicationStage(ctx, lease, request, runner, session)
	if err != nil || !moduleStageResultMatches(observation, request, review.NativePreflightResponse()) {
		_, _ = state.RecordPublicationUnknown(context.WithoutCancel(ctx), attempt)
		return publicationStageEvidence{}, datapublish.ErrInspection
	}
	if action == moduleStagePreflight {
		if _, err = state.RecordPublicationPreflight(ctx, attempt, moduleStageProtectedObservation(lease, observation)); err != nil {
			_, _ = state.RecordPublicationUnknown(context.WithoutCancel(ctx), attempt)
			return publicationStageEvidence{}, datapublish.ErrInspection
		}
	}
	if action == moduleStagePublish || action == moduleStageCreate {
		observed := store.PublicationStageObservation{Process: moduleStageProtectedObservation(lease, observation), NativeDatabase: observation.Database, NativePublisher: observation.Publisher, ProgramWireHash: observation.ProgramWireHash, SchemaSHA256: observation.SchemaSHA256, Receipt: slices.Clone(observation.Receipt), ReceiptBindingSHA256: observation.ReceiptBindingSHA}
		if _, err = state.RecordPublicationPublished(ctx, attempt, observed); err != nil {
			_, _ = state.RecordPublicationUnknown(context.WithoutCancel(ctx), attempt)
			return publicationStageEvidence{}, datapublish.ErrInspection
		}
	}
	return publicationStageEvidence{lease: lease, source: lease.restored, scope: cursor.Scope(), observation: observation, stageRoot: lease.rootMember}, nil
}

func moduleStageResultMatches(observation moduleStageObservation, request moduleStageInput, reviewedPreflight []byte) bool {
	runtime := request.Runtime
	if observation.Action != request.Action || observation.CredentialSHA256 != runtime.Secrets.Publisher.SHA256 || !observation.ChildStopped {
		return false
	}
	if request.Action == moduleStageCreate {
		return runtime.State == data.EngineUnpublished && data.ValidCatalogHash(observation.Database) && observation.Publisher == request.ExpectedPublisher && observation.PublicationKnown && len(observation.Receipt) != 0 && data.ValidCatalogHash(observation.ReceiptBindingSHA) && observation.ProgramWireHash == request.ProgramWireHash && observation.SchemaSHA256 == request.SchemaSHA256 && len(observation.Preflight) == 0 && len(reviewedPreflight) == 0
	}
	if observation.Database != string(runtime.Database) || observation.Publisher != string(runtime.Publisher) {
		return false
	}
	if request.Action == moduleStagePreflight {
		return !observation.PublicationKnown && len(observation.Receipt) == 0 && observation.ReceiptBindingSHA == "" && observation.ProgramWireHash == string(runtime.Module.Program) && observation.SchemaSHA256 == runtime.Module.Schema.SHA256 && slices.Equal(observation.Preflight, reviewedPreflight)
	}
	return request.Action == moduleStagePublish && observation.PublicationKnown && len(observation.Receipt) != 0 && data.ValidCatalogHash(observation.ReceiptBindingSHA) && observation.ProgramWireHash == request.ProgramWireHash && observation.SchemaSHA256 == request.SchemaSHA256 && slices.Equal(observation.Preflight, reviewedPreflight)
}

func moduleStageCursorStep(action moduleStageAction) (datapublish.Step, error) {
	switch action {
	case moduleStagePreflight:
		return datapublish.CheckPreflight, nil
	case moduleStagePublish:
		return datapublish.PublishStage, nil
	case moduleStageCreate:
		return datapublish.PublishStage, nil
	default:
		return "", datapublish.ErrRecord
	}
}

func moduleStageProtectedObservation(lease *publicationStageLease, observation moduleStageObservation) store.PublicationReviewObservation {
	return store.PublicationReviewObservation{Preflight: slices.Clone(observation.Preflight), PublisherProof: slices.Clone(observation.PublisherProof), Runtime: lease.permit.Runtime.Clone(), Publisher: observation.Publisher,
		HelperPID: lease.helperPID, HelperStartTicks: lease.helperTicks, HelperCgroup: lease.helperCgroup, ChildPID: observation.ChildPID, ChildStartTicks: observation.ChildStartTicks, StartedAt: lease.startedAt, FinishedAt: lease.finishedAt, Disposed: lease.disposed && observation.ChildStopped && observation.ChildBound}
}
