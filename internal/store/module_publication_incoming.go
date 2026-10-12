package store

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

// ClaimPublicationIncomingRepository opens the already reserved fresh prefix
// only after real publication, passive validation and helper disposal. The
// returned17 lease journals exactly one initialization; unknown results are
// inspected through that lease and cannot reissue repository creation.
func (s *Store) ClaimPublicationIncomingRepository(ctx context.Context, attempt *PublicationAttempt) (*NativeIncomingRepository, error) {
	if !attempt.active(s) || attempt.restored.unchanged(ctx) != nil {
		return nil, ErrConflict
	}
	tx, cancel, err := s.beginWrite(ctx)
	defer cancel()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.readPublicationAttemptTx(ctx, tx, attempt)
	step, ready := current.cursor.NextStep()
	if err != nil || !ready || step != datapublish.FinalTransition {
		return nil, errors.Join(ErrConflict, err)
	}
	if _, _, err = readPublicationDisposalTx(ctx, tx, attempt, current); err != nil {
		return nil, err
	}
	consumer, err := readNativeModulePublishConsumerTx(ctx, tx, current.point.view.PointID, attempt.planID, attempt.operationID)
	if err != nil {
		return nil, err
	}
	intent, err := readNativeRepositoryIntentTx(ctx, tx, current.claim.RepositoryIntentID)
	if err != nil || intent.hash != current.claim.RepositoryIntentSHA {
		return nil, errors.Join(ErrConflict, err)
	}
	incoming, err := s.claimNativeRepositoryCreationTx(ctx, tx, attempt.preparation.guard, consumer, intent)
	if err != nil || !attempt.active(s) || attempt.restored.unchanged(ctx) != nil {
		return nil, errors.Join(ErrConflict, err)
	}
	return incoming, tx.Commit()
}
