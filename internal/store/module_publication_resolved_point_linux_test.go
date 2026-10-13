//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/ops"
)

func resolvedPublicationPointFixture(t *testing.T) (*Store, *NativePreparation, NativePreparedPointView, string) {
	t.Helper()
	s, prep, _, point := nativeCompletedPointStateFixture(t, ops.RecoveryRequired)
	ctx := context.Background()
	original, err := s.GetOperation(ctx, prep.intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, originalHash, err := nativeBackupOriginal(ctx, s.db, original.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	i := &NativeBackupInspection{state: s, guard: prep.preparation.guard, original: original, binding: point.BindingID, originalSHA256: originalHash}
	review, err := i.Review(ctx, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	resolver, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: original.App, RecoveryOf: original.ID, SecretRef: review.ID}, original.Requester, "resolved-publication-point")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, resolver.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	var rawPoint []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_prepared_points WHERE id=?", point.PointID).Scan(&rawPoint); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(nativecapture.TaskReceipt{Kind: string(ops.NativePrepare), OperationID: original.ID, BindingID: string(point.BindingID), EpochID: string(point.EpochID), PointID: point.PointID, PointSHA256: nativePointHash(rawPoint), Outcome: "verified", Engine: "spacetimedb", CaptureIntervalSeconds: 30, Loss: "unknown"})
	completed, _ := json.Marshal(nativecapture.BackupResolutionReceipt{Kind: string(ops.NativeBackupResolve), OperationID: resolver.ID, ReviewID: review.ID, OriginalOperationID: original.ID, OriginalKind: string(original.Kind), BindingID: string(point.BindingID), EpochID: string(point.EpochID), OriginalSHA256: originalHash, ObservationSHA256: review.ObservationSHA256, CompletionSHA256: nativePointHash(receipt), Receipt: receipt, Outcome: "adopted"})
	claim, _ := json.Marshal(review)
	if _, err := s.db.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,'claimed',?)", resolver.ID, claim); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"observed", "completed"} {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO data_native_backup_resolution_events VALUES(?,?,?)", resolver.ID, stage, completed); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CompleteTask(ctx, resolver.ID, ops.Succeeded, ops.TaskOutcome{Receipt: completed}); err != nil {
		t.Fatal(err)
	}
	return s, prep, point, resolver.ID
}

func TestPublicationReviewUsesValidatedResolvedPointWithoutChangingOriginal(t *testing.T) {
	s, prep, point, _ := resolvedPublicationPointFixture(t)
	ctx := context.Background()
	before, err := s.GetOperation(ctx, prep.intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.ReadTaskOutcome(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := json.Marshal(outcome)
	artifact, err := datapublish.NewArtifact([]byte{0, 'a', 's', 'm', 1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.beginPublicationReview(ctx, prep.preparation, point.PointID, artifact)
	if err != nil || s.CheckPublicationReviewPreparation(ctx, review) != nil {
		t.Fatal("validated resolved point could not begin and recheck publication review", err)
	}
	after, err := s.GetOperation(ctx, before.ID)
	if err != nil || after != before || after.State != ops.RecoveryRequired {
		t.Fatal("review changed original failure", err)
	}
	actual, err := s.ReadTaskOutcome(ctx, before.ID)
	afterRaw, _ := json.Marshal(actual)
	if err != nil || !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("review rewrote original outcome", err)
	}
}

func TestPublicationReviewRefusesRecoveryRequiredPointWithoutResolution(t *testing.T) {
	s, prep, _, point := nativeCompletedPointStateFixture(t, ops.RecoveryRequired)
	artifact, err := datapublish.NewArtifact([]byte{0, 'a', 's', 'm', 1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.beginPublicationReview(context.Background(), prep.preparation, point.PointID, artifact); !errors.Is(err, ErrConflict) {
		t.Fatal("failed original alone supplied publication authority", err)
	}
}

func TestPublicationReviewRefusesChangedResolverProof(t *testing.T) {
	s, prep, point, resolver := resolvedPublicationPointFixture(t)
	ctx := context.Background()
	artifact, err := datapublish.NewArtifact([]byte{0, 'a', 's', 'm', 1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.beginPublicationReview(ctx, prep.preparation, point.PointID, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER data_native_backup_resolution_events_no_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE data_native_backup_resolution_events SET canonical='{}' WHERE operation_id=? AND stage='completed'", resolver); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPublicationReviewPreparation(ctx, review); err == nil {
		t.Fatal("changed resolver proof retained publication authority")
	}
}
