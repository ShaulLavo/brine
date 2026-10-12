//go:build linux

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

func TestModulePublicationReviewCapabilityRequiresActualCompletedPointAndRetainedGuard(t *testing.T) {
	state, preparation, point := nativeCompletedPointFixture(t)
	ctx := context.Background()
	artifact, err := datapublish.NewArtifact([]byte{0, 'a', 's', 'm', 1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	retained, err := state.ReadNativePreparedPoint(ctx, point.PointID)
	if err != nil {
		t.Fatal(err)
	}
	review, err := state.beginPublicationReview(ctx, preparation.preparation, point.PointID, artifact)
	if err != nil || state.CheckPublicationReviewPreparation(ctx, review) != nil {
		t.Fatal("actual protected completed point and held guard could not begin review", err)
	}
	if review.pointSHA != retained.PointSHA256 || review.requester == "" || review.binding != point.BindingID || review.artifact.WasmSHA256() != artifact.WasmSHA256() {
		t.Fatal("review capability did not derive actual protected point/candidate bindings")
	}
	if err = state.CheckPublicationReviewPreparation(ctx, &PublicationReviewPreparation{}); !errors.Is(err, ErrConflict) {
		t.Fatal("zero record manufactured review authority", err)
	}
	other, err := OpenContext(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if err = other.CheckPublicationReviewPreparation(ctx, review); !errors.Is(err, ErrConflict) {
		t.Fatal("capability crossed its protected store", err)
	}
	if err = preparation.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	if err = state.CheckPublicationReviewPreparation(ctx, review); !errors.Is(err, ErrConflict) {
		t.Fatal("released real guard retained review authority", err)
	}
}
