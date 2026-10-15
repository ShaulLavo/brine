//go:build linux

package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"strings"
	"testing"
	"time"
)

func unstoppedPreparationFixture(t *testing.T, recordFrontend ...bool) (*Store, *NativePreparation, ops.Operation) {
	t.Helper()
	ctx := t.Context()
	s, p, deploy := managedControlStateFixtureAtRoot(t, true, false, "")
	wanted := commitAbandonFixtureRelease(t, s, p, deploy)
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepare, BindingID: wanted.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: p.App, SecretRef: ref}, requester.String(), "unstopped-prepare")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	fence, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := s.AcquireEnginePreparation(ctx, wanted.BindingID, fence.ID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	prep, err := s.BeginNativePreparation(ctx, guard, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	routes := NativePreparedRoutes{PointID: prep.PointID(), OperationID: op.ID, App: p.App, PreviousGeneration: 1, PreviousFiles: map[string]string{p.App + ".caddy": "sha256:" + strings.Repeat("b", 64)}, CurrentGeneration: 2, CurrentFiles: map[string]string{}}
	if err = s.RecordNativePreparedRoutes(ctx, prep, routes, false); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordNativePreparedRoutes(ctx, prep, routes, true); err != nil {
		t.Fatal(err)
	}
	if len(recordFrontend) == 0 || recordFrontend[0] {
		if err = s.RecordNativePreparedFrontendStop(ctx, prep, false); err != nil {
			t.Fatal(err)
		}
		if err = s.RecordNativePreparedFrontendStop(ctx, prep, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CompleteTask(ctx, op.ID, ops.RecoveryRequired, ops.TaskOutcome{Error: result.InternalError}); err != nil {
		t.Fatal(err)
	}
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}
	op, err = s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, prep, op
}

func TestNativePreparationBeforeEngineStopCanBeInspected(t *testing.T) {
	s, _, op := unstoppedPreparationFixture(t)
	inspection, err := s.OpenNativeBackupInspection(t.Context(), op.ID, op.Requester)
	if err != nil {
		t.Fatal("preparation without an issued engine stop cannot be inspected", err)
	}
	defer func() { _ = inspection.Close() }()
}

func claimUnstoppedResolution(t *testing.T, s *Store, original ops.Operation) (*NativeBackupInspection, string) {
	t.Helper()
	inspection, err := s.OpenNativeBackupInspection(t.Context(), original.ID, original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := inspection.PreparedExpectation(t.Context())
	if err != nil || !expected.StopNotIssued {
		t.Fatal("unissued stop expectation", err)
	}
	hash := strings.Repeat("a", 64)
	review, err := inspection.Review(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if err = inspection.Close(); err != nil {
		t.Fatal(err)
	}
	resolver, _, err := s.CreateOperation(t.Context(), ops.Intent{Kind: ops.NativeBackupResolve, App: original.App, SecretRef: review.ID, RecoveryOf: original.ID}, original.Requester, "release-unissued-stop")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetOperationState(t.Context(), resolver.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	inspection, err = s.ClaimNativeBackupResolution(t.Context(), resolver.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Close() })
	return inspection, hash
}

func TestNativeUnissuedStopResolutionReleasesOnlyOriginalFenceAndPreservesFailure(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprint(running), func(t *testing.T) { testUnstoppedRelease(t, running) })
	}
}

func testUnstoppedRelease(t *testing.T, running bool) {
	s, prep, original := unstoppedPreparationFixture(t)
	before, err := s.ReadTaskOutcome(t.Context(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	inspection, hash := claimUnstoppedResolution(t, s, original)
	if _, err = inspection.ReleaseUnstoppedPreparation(t.Context(), strings.Repeat("b", 64), running, strings.Repeat("c", 64), strings.Repeat("d", 64)); err == nil {
		t.Fatal("changed review released fence")
	}
	receipt, err := inspection.ReleaseUnstoppedPreparation(t.Context(), hash, running, strings.Repeat("c", 64), strings.Repeat("d", 64))
	if err != nil {
		t.Fatal("reviewed unissued stop release", err)
	}
	committed, err := s.GetOperation(t.Context(), inspection.resolver)
	if err != nil || committed.State != ops.Succeeded {
		t.Fatal("fence release was not atomic with resolver success", err)
	}
	raw, _ := json.Marshal(receipt)
	if err = s.CompleteTask(t.Context(), inspection.resolver, ops.Succeeded, ops.TaskOutcome{Receipt: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err = readNativeBackupResolvedReceipt(t.Context(), s.db, original); err != nil {
		t.Fatal("released producer could not be read", err)
	}
	if _, err = s.ReadNativePreparedPoint(t.Context(), prep.PointID()); !errors.Is(err, ErrNotFound) {
		t.Fatal("release invented a backup point", err)
	}
	wanted, err := s.ReadManagedDesired(t.Context(), prep.intent.BindingID)
	if err != nil || wanted.EngineRunning != running || wanted.FrontendRunning || wanted.IngressServing {
		t.Fatal("release resumed components", wanted, err)
	}
	route, err := s.ReadManagedRouteState(t.Context(), original.App)
	if err != nil || route.Present || route.Generation != 2 {
		t.Fatal("withdrawn route was not committed", route, err)
	}
	if err = refusePendingNativePreparation(t.Context(), s.db, prep.intent.BindingID, ""); err != nil {
		t.Fatal("resolved original still blocks next preparation", err)
	}
	after, err := s.GetOperation(t.Context(), original.ID)
	afterOutcome, outcomeErr := s.ReadTaskOutcome(t.Context(), original.ID)
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(afterOutcome)
	if err != nil || outcomeErr != nil || after != original || !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("original failure changed", err, outcomeErr)
	}
	if _, err = inspection.ReleaseUnstoppedPreparation(t.Context(), hash, running, strings.Repeat("c", 64), strings.Repeat("d", 64)); err == nil {
		t.Fatal("released preparation resolved twice")
	}
}

func TestNativeUnissuedStopResolutionRollsBackWithFenceRelease(t *testing.T) {
	s, prep, original := unstoppedPreparationFixture(t)
	inspection, hash := claimUnstoppedResolution(t, s, original)
	before, err := s.ReadManagedDesired(t.Context(), prep.intent.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(t.Context(), `CREATE TRIGGER reject_unstopped_release BEFORE UPDATE ON data_fences BEGIN SELECT RAISE(ABORT,'fixture'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err = inspection.ReleaseUnstoppedPreparation(t.Context(), hash, false, strings.Repeat("c", 64), strings.Repeat("d", 64)); err == nil {
		t.Fatal("failed fence release committed disposition")
	}
	after, err := s.ReadManagedDesired(t.Context(), prep.intent.BindingID)
	if err != nil || after != before {
		t.Fatal("failed release changed desired state", err)
	}
	var count int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM data_native_backup_resolution_events WHERE operation_id=? AND stage='completed'", inspection.resolver).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed release left completion", count, err)
	}
}

func TestNativePreparationWithIssuedUnknownStopRefusesRelease(t *testing.T) {
	s, prep, original := unstoppedPreparationFixture(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	proof, _ := json.Marshal(NativeStopIntent{Mode: "managed_clean_stop", ContainerID: strings.Repeat("a", 64), PID: 7, StartTicks: 9, ObservedAt: time.Now().UTC()})
	if err = appendNativePreparationProof(t.Context(), tx, prep.intent, prep.raw, "stop_intent", proof); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if inspection, err := s.OpenNativeBackupInspection(t.Context(), original.ID, original.Requester); err == nil {
		_ = inspection.Close()
		t.Fatal("unknown issued stop admitted release")
	}
}

func TestNativePreparationBeforeFrontendStopCanBeInspected(t *testing.T) {
	s, _, op := unstoppedPreparationFixture(t, false)
	inspection, err := s.OpenNativeBackupInspection(t.Context(), op.ID, op.Requester)
	if err != nil {
		t.Fatal("preparation without a frontend stop intent cannot be inspected", err)
	}
	defer func() { _ = inspection.Close() }()
	expected, err := inspection.PreparedExpectation(t.Context())
	if err != nil || !expected.StopNotIssued {
		t.Fatal("unissued frontend and engine stops", err)
	}
}
