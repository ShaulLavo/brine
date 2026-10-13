//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func previousResolutionFixture(t *testing.T) (*Store, ops.Operation, ops.Operation) {
	t.Helper()
	s, prep, life, op := nativePreparationFixture(t)
	ctx := context.Background()
	if _, err := s.RecordNativeStoppedPreparation(ctx, prep, life, nativeAbsentStopFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, op.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	if err := life.Release(); err != nil {
		t.Fatal(err)
	}
	if err := prep.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := s.OpenNativeBackupInspection(ctx, op.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	review, err := inspection.Review(ctx, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	resolver, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: original.App, SecretRef: review.ID, RecoveryOf: original.ID}, original.Requester, "previous-resolution")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, resolver.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	inspection, err = s.ClaimNativeBackupResolution(ctx, resolver.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTask(ctx, resolver.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	resolver, err = s.GetOperation(ctx, resolver.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, original, resolver
}

func TestPreviousResolutionClaimsRemainBoundWhenCurrentClaimIsExcluded(t *testing.T) {
	s, original, failed := previousResolutionFixture(t)
	ctx := context.Background()
	before, err := s.ReadTaskOutcome(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := json.Marshal(before)
	var claimed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id=? AND stage='claimed'", failed.ID).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	inspection, err := s.OpenNativeBackupInspection(ctx, original.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := inspection.PreviousResolutionClaimsSHA256(ctx)
	if err != nil || !data.ValidCatalogHash(digest) {
		t.Fatal("failed claim was not bound", err)
	}
	review, err := inspection.Review(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativeBackupResolve, App: original.App, SecretRef: review.ID, RecoveryOf: original.ID}, original.Requester, "fresh-current-resolution")
	if err != nil {
		t.Fatal(err)
	}
	inspection, err = s.OpenNativeBackupInspection(ctx, original.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspection.PreviousResolutionClaimsSHA256(ctx); !errors.Is(err, ErrConflict) {
		t.Fatal("unclaimed active resolver silently excluded", err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, current.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	inspection, err = s.ClaimNativeBackupResolution(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inspection.Close() }()
	currentDigest, err := inspection.PreviousResolutionClaimsSHA256(ctx)
	if err != nil || currentDigest != digest {
		t.Fatal("current opaque claim rebound previous evidence", err)
	}
	if err := inspection.CheckReviewedObservation(ctx, currentDigest); err != nil {
		t.Fatal(err)
	}
	actualClaimant := inspection.resolver
	inspection.resolver = failed.ID
	if _, err := inspection.PreviousResolutionClaimsSHA256(ctx); !errors.Is(err, ErrConflict) {
		t.Fatal("caller-selected historical failed claimant was excluded", err)
	}
	inspection.resolver = actualClaimant
	unchangedOriginal, err := s.GetOperation(ctx, original.ID)
	if err != nil || unchangedOriginal != original {
		t.Fatal("original failed producer was changed", err)
	}

	after, err := s.ReadTaskOutcome(ctx, failed.ID)
	afterRaw, _ := json.Marshal(after)
	if err != nil || !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("old failure outcome changed", err)
	}
	var afterClaim []byte
	if err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_backup_resolution_events WHERE operation_id=? AND stage='claimed'", failed.ID).Scan(&afterClaim); err != nil || !bytes.Equal(claimed, afterClaim) {
		t.Fatal("old immutable claim changed", err)
	}
	beforeOp, err := s.GetOperation(ctx, failed.ID)
	if err != nil || beforeOp != failed {
		t.Fatal("old failed resolver changed", err)
	}
	if _, err := s.db.ExecContext(ctx, "DROP TRIGGER operation_outcomes_immutable"); err != nil {
		t.Fatal(err)
	}
	changed, _ := json.Marshal(ops.TaskOutcome{Error: result.PolicyRefused})
	if _, err := s.db.ExecContext(ctx, "UPDATE operation_outcomes SET canonical=? WHERE operation_id=?", changed, failed.ID); err != nil {
		t.Fatal(err)
	}
	changedDigest, err := inspection.PreviousResolutionClaimsSHA256(ctx)
	if err != nil || changedDigest == digest {
		t.Fatal("changed valid previous failure escaped digest", err)
	}
	if err := inspection.CheckReviewedObservation(ctx, changedDigest); !errors.Is(err, ErrConflict) {
		t.Fatal("fresh review adopted changed prior failure", err)
	}
}

func TestPreviousResolutionClaimsRefuseChangedClaimAndForeignScope(t *testing.T) {
	for _, kind := range []string{"claim", "requester"} {
		t.Run(kind, func(t *testing.T) { checkPreviousResolutionCorruption(t, kind) })
	}
}

func checkPreviousResolutionCorruption(t *testing.T, kind string) {
	t.Helper()
	s, original, failed := previousResolutionFixture(t)
	ctx := context.Background()
	inspection, err := s.OpenNativeBackupInspection(ctx, original.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = inspection.Close() }()
	if _, err := inspection.PreviousResolutionClaimsSHA256(ctx); err != nil {
		t.Fatal(err)
	}
	if kind == "claim" {
		if _, err := s.db.ExecContext(ctx, "DROP TRIGGER data_native_backup_resolution_events_no_update"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE data_native_backup_resolution_events SET canonical=json_set(canonical,'$.observation_sha256',?) WHERE operation_id=? AND stage='claimed'", strings.Repeat("b", 64), failed.ID); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := s.db.ExecContext(ctx, "DROP TRIGGER operation_identity"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE operations SET requester='foreign-requester' WHERE id=?", failed.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := inspection.PreviousResolutionClaimsSHA256(ctx); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign or mismatched previous claim supplied authority", err)
	}
}
