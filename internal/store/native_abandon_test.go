//go:build linux

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeAbandonAdmissionRejectsUninitializedPoint(t *testing.T) {
	ctx := context.Background()
	s, preparation, point := nativeCompletedPointFixture(t)
	original, err := s.GetOperation(ctx, preparation.intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	requester, err := datainit.ParseRequester(original.Requester)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepareAbandon, BindingID: point.BindingID, PointID: point.PointID})
	if err != nil {
		t.Fatal(err)
	}
	abandon, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepareAbandon, App: original.App, SecretRef: ref}, original.Requester, "reject-uninitialized")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, abandon.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenNativeAbandonment(ctx, abandon.ID); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("uninitialized point admitted: %v", err)
	}
}

func TestNativeAbandonPinnedRecoveryFixtureArtifact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "spacetimedb", "testdata", "recovery-v1.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc" {
		t.Fatal("pinned recovery fixture artifact changed")
	}
}

func TestNativeAbandonInitializedPointFixtureRetainsClosedArm(t *testing.T) {
	_, _, point := nativeCompletedPointFixture(t)
	point.SourceKind = "initialized"
	point.CommittedOffsetKnown = true
	point.CommittedOffset = 8
	point.NativeDatabase = strings.Repeat("1", 64)
	point.NativePublisher = strings.Repeat("2", 64)
	point.ProgramWireHash = strings.Repeat("3", 64)
	point.ArtifactWasmSHA256 = "92f4efff417d8f17ba4329c46fd1bdc13c8e234137e6150ac33f62503cb212cc"
	point.SchemaSHA256 = "f10d1efcbbdcec34434fa257946ca8a562ba1ad5f6b1b22b17fca14ca7eacc45"
	point.Names = []string{"game"}
	if !validNativePreparedPoint(point) {
		t.Fatal("initialized point fixture is not closed")
	}
}

func TestNativeAbandonStoppedIntentAppendIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, p, deploy := managedControlFixture(t)
	binding := p.Managed.Database.BindingID
	previous := ManagedDesiredState{BindingID: binding, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: deploy.ID, EngineRunning: true, IngressServing: true}
	if _, err := s.CommitManagedDesired(ctx, deploy.ID, 0, previous); err != nil {
		t.Fatal(err)
	}
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("2", 64))
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.SaveNativeTaskInput(ctx, NativeTaskInput{Requester: requester, Kind: ops.NativePrepareAbandon, BindingID: binding, PointID: strings.Repeat("3", 32)})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepareAbandon, App: p.App, SecretRef: ref}, requester.String(), "atomic-abandon")
	if err != nil {
		t.Fatal(err)
	}
	facts := nativeAbandonFacts{point: nativePreparedPoint{view: NativePreparedPointView{BindingID: binding, EpochID: previous.EpochID, RuntimeSHA256: previous.RuntimeSHA256}}, wanted: previous, release: Release{PlanID: p.Hash}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendNativeAbandonStoppedIntent(ctx, tx, op, facts); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadManagedDesired(ctx, binding); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM data_engine_desired_states WHERE binding_id=?", binding).Scan(&count); err != nil || count != 1 {
		t.Fatal("rolled back stopped intent", err, count)
	}
	// A second transaction commits the same private append and proves the old
	// running row remains preserved while the new revision is stopped.
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	wanted, err := appendNativeAbandonStoppedIntent(ctx, tx, op, facts)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if wanted.Revision != 2 || wanted.EngineRunning || wanted.FrontendRunning || wanted.IngressServing || wanted.OperationID != op.ID {
		t.Fatal("stopped intent shape changed", wanted)
	}
	current, err := s.ReadManagedDesired(ctx, binding)
	if err != nil || current != wanted {
		t.Fatal("stopped intent not current", current, err)
	}
}
