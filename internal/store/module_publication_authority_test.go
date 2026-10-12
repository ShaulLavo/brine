package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPublicationVerificationAndTransitionRequireRetainedAuthority(t *testing.T) {
	ctx := context.Background()
	state, err := OpenContext(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = state.Close() }()
	if _, err = state.RecordPublicationVerified(ctx, nil, PublicationVerificationObservation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("decoded verification syntax admitted without an actual retained restore", err)
	}
	if _, err = state.RecordPublicationDisposed(ctx, &PublicationAttempt{}); !errors.Is(err, ErrConflict) {
		t.Fatal("empty attempt granted disposal authority", err)
	}
	if _, err = state.ClaimPublicationIncomingRepository(ctx, &PublicationAttempt{}); !errors.Is(err, ErrConflict) {
		t.Fatal("empty attempt granted repository creation authority", err)
	}
	if _, err = state.BeginPublicationTargetPreparation(ctx, &PublicationAttempt{}, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("empty attempt admitted a transition destination", err)
	}
	if _, err = state.ReadPublicationTargetFacts(ctx, &PublicationTargetPreparation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("decoded target syntax admitted as held preparation", err)
	}
	if err = state.RecordPublicationTarget(ctx, nil, PublicationTargetObservation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("unowned descriptors admitted transition target", err)
	}
	if _, err = state.ReadPublicationTransition(ctx, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("unowned transition became executable", err)
	}
	if _, err = state.ActivatePublicationTransition(ctx, nil, PublicationTransitionObservation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("raw host syntax activated an epoch", err)
	}
	if _, err = state.CompletePublicationTransition(ctx, nil, PublicationTransitionObservation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("raw completion released a fence", err)
	}
	if _, err = state.ReadPublicationPrivateInspectionFacts(ctx, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("raw private inspection gained source facts", err)
	}
	if _, err = state.PreservePublicationOriginalSource(ctx, nil, PublicationSourcePreservationObservation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("raw private inspection released source fence", err)
	}
	if _, err = json.Marshal(PublicationVerificationObservation{}); err == nil {
		t.Fatal("private passive observation became a request payload")
	}
	if _, err = json.Marshal(PublicationTargetPreparation{}); err == nil {
		t.Fatal("held target capability became a request payload")
	}
	var proofs int
	if err = state.db.QueryRowContext(ctx, "SELECT count(*) FROM data_module_publication_proofs").Scan(&proofs); err != nil || proofs != 0 {
		t.Fatal("refused authority changed protected proofs", proofs, err)
	}
}
