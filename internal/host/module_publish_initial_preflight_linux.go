//go:build linux

package host

import (
	"context"
	"slices"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/store"
)

func recordInitialPublicationPreflight(ctx context.Context, working *publicationWorkingStage) error {
	if working.unchanged(ctx) != nil || working.source.permit.Runtime.State != data.EngineUnpublished || working.source.empty == nil {
		return datapublish.ErrRecord
	}
	owner, err := working.source.state.ReadNativeInitialOwner(ctx, working.source.empty.stopped)
	if err != nil || owner.Publisher != working.source.initialPublisher || owner.PublisherProofSHA256 != working.source.publisherProofSHA {
		return datapublish.ErrRecord
	}
	observed := store.PublicationReviewObservation{Runtime: working.source.permit.Runtime.Clone(), Publisher: owner.Publisher, PublisherProof: slices.Clone(owner.PublisherProof)}
	_, err = working.source.state.RecordPublicationPreflight(ctx, working.attempt, observed)
	return err
}
