//go:build linux

package host

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationStageEvidence struct {
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
	if action == moduleStagePublish {
		request.PreflightSHA256, request.PreflightTokenSHA = plan.Preflight().ResponseSHA256, plan.Preflight().TokenSHA256
	}
	observation, err := runPublicationStage(ctx, lease, request, runner, session)
	if err != nil {
		_, _ = state.RecordPublicationUnknown(context.WithoutCancel(ctx), attempt)
		return publicationStageEvidence{}, datapublish.ErrInspection
	}
	if observation.Action != action || observation.Database != string(lease.permit.Runtime.Database) || observation.Publisher != string(lease.permit.Runtime.Publisher) || observation.CredentialSHA256 != lease.permit.Runtime.Secrets.Publisher.SHA256 || !observation.ChildStopped {
		return publicationStageEvidence{}, datapublish.ErrInspection
	}
	if action == moduleStagePreflight && !slices.Equal(observation.Preflight, review.NativePreflightResponse()) {
		return publicationStageEvidence{}, datapublish.ErrStale
	}
	return publicationStageEvidence{source: lease.restored, scope: cursor.Scope(), observation: observation, stageRoot: lease.rootMember}, nil
}

func moduleStageCursorStep(action moduleStageAction) (datapublish.Step, error) {
	switch action {
	case moduleStagePreflight:
		return datapublish.CheckPreflight, nil
	case moduleStagePublish:
		return datapublish.PublishStage, nil
	default:
		return "", datapublish.ErrRecord
	}
}
