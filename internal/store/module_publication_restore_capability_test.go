package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/nativeevidence"
	"github.com/ShaulLavo/brine/internal/restic"
)

func TestModulePublicationReviewCannotReplaceAuthenticatedDownloadWithSyntax(t *testing.T) {
	state, err := OpenContext(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if err = state.BindPublicationReviewRestore(context.Background(), &PublicationReviewPreparation{}, restic.AuthenticatedRestore{}, nil, nativeevidence.Evidence{}); !errors.Is(err, ErrConflict) {
		t.Fatal("unowned decoded restore syntax manufactured review provenance", err)
	}
	if planned, err := state.CompletePublicationReview(context.Background(), &PublicationReviewPreparation{}, PublicationReviewObservation{}); !errors.Is(err, ErrConflict) || planned.ID() != "" {
		t.Fatal("unowned host declaration manufactured approval", err)
	}
}
