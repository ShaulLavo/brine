//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/store"
)

type publicationTargetVerification struct {
	target       publicationTargetCheckpoint
	evidence     nativeevidence.Evidence
	disposal     nativeDisposal
	pairs        spacetimedb.RecoveredPairs
	initial      *spacetimedb.RecoveryInitialProof
	preservation *spacetimedb.RecoveryPreservationProof
}

func verifyPublicationTarget(ctx context.Context, target publicationTargetCheckpoint, session localexec.Session) (_ publicationTargetVerification, err error) {
	if target.unchanged(ctx) != nil {
		return publicationTargetVerification{}, datapublish.ErrRecord
	}
	cursor, err := target.stage.source.state.ReadPublicationAttemptCursor(ctx, target.stage.lease.working.attempt)
	if err != nil || cursor.State() != datapublish.IntentRecorded || cursor.IntendedStep() != datapublish.VerifyStage {
		return publicationTargetVerification{}, datapublish.ErrRecord
	}
	review, err := target.stage.source.state.ReadPublicationAttemptReview(ctx, target.stage.lease.working.attempt)
	if err != nil {
		return publicationTargetVerification{}, err
	}
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return publicationTargetVerification{}, err
	}
	view, err := target.readView(ctx)
	if err != nil {
		return publicationTargetVerification{}, err
	}
	defer func() { err = errors.Join(err, view.close()) }()
	evidence, disposal, err := readNativeView(ctx, session, pin, view, target.checkpoint)
	if err != nil || disposal.nonce != view.nonce || disposal.viewHash != view.seal.hash || disposal.imageID != pin.ImageID || disposal.at.IsZero() || target.unchanged(ctx) != nil {
		return publicationTargetVerification{}, datapublish.ErrInspection
	}
	if err = view.close(); err != nil {
		return publicationTargetVerification{}, datapublish.ErrInspection
	}
	current, err := readNativeReaderPin(ctx)
	if err != nil || current != pin {
		return publicationTargetVerification{}, datapublish.ErrRecord
	}
	pairs, err := spacetimedb.RecoveryPairsFromPassive(evidence, review.Plan().Candidate().Schema())
	if err != nil {
		return publicationTargetVerification{}, err
	}
	verified := publicationTargetVerification{target: target, evidence: evidence, disposal: disposal, pairs: pairs}
	if review.Plan().Source().Kind() == datapublish.CreateSource {
		initial, err := spacetimedb.CheckRecoveryPairsInitial(pairs)
		if err != nil {
			return publicationTargetVerification{}, err
		}
		verified.initial = &initial
		return recordPublicationTargetVerification(ctx, verified)
	}
	source, err := spacetimedb.RecoveryPairsFromPassive(target.stage.source.evidence, review.Plan().Source().Schema())
	if err != nil {
		return publicationTargetVerification{}, err
	}
	preserved, err := spacetimedb.CheckRecoveryPairPreservation(source, pairs)
	if err != nil {
		return publicationTargetVerification{}, err
	}
	verified.preservation = &preserved
	return recordPublicationTargetVerification(ctx, verified)
}

func recordPublicationTargetVerification(ctx context.Context, verified publicationTargetVerification) (publicationTargetVerification, error) {
	if verified.target.unchanged(ctx) != nil {
		return publicationTargetVerification{}, datapublish.ErrRecord
	}
	stage := verified.target.stage
	_, err := stage.source.state.RecordPublicationVerified(ctx, stage.lease.working.attempt, store.PublicationVerificationObservation{Checkpoint: verified.target.checkpoint, Evidence: verified.evidence, ReaderNonce: verified.disposal.nonce, ReaderViewSHA256: verified.disposal.viewHash, ReaderImageID: verified.disposal.imageID, ReaderDisposedAt: verified.disposal.at})
	if err != nil {
		return publicationTargetVerification{}, err
	}
	return verified, nil
}
