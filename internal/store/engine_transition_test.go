//go:build linux

package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type nativeTransitionFixture struct {
	store       *Store
	input       data.EngineTransition
	preparation *EnginePreparation
	runtime     data.EngineRuntime
	allocation  data.AllocationReceipt
}

func storeTransitionEvidence(scope data.EngineTransitionScope, epoch data.ReplicaEpochID, kind data.EngineEvidenceKind, ordinal int) data.EngineEvidenceRef {
	return data.EngineEvidenceRef{Kind: kind, ID: fmt.Sprintf("%032x", ordinal), SHA256: fmt.Sprintf("%064x", ordinal), DatabaseID: scope.DatabaseID, BindingID: scope.BindingID, EpochID: epoch}
}

func newNativeTransitionFixture(t *testing.T) nativeTransitionFixture {
	return nativeTransitionStateFixture(t, data.EngineInitialized)
}

func nativeTransitionStateFixture(t *testing.T, state data.EngineDataState) nativeTransitionFixture {
	t.Helper()
	ctx := context.Background()
	s := openTest(t)
	reserved, err := s.ReserveEngine(ctx, engineReservation(t))
	if err != nil {
		t.Fatal(err)
	}
	if err = data.PrepareEngineLocks(ctx, s.dir, reserved.Database.BindingID); err != nil {
		t.Fatal(err)
	}
	runtime := engineRuntimeFixture(reserved)
	if err = s.RegisterEngineModule(ctx, runtime.BindingID, runtime.Module); err != nil {
		t.Fatal(err)
	}
	saveEngineAllocationFixture(t, s, reserved)
	fence, err := s.HoldDataFence(ctx, reserved.Database.DatabaseID, "prepare-engine")
	if err != nil {
		t.Fatal(err)
	}
	current := runtime.Clone()
	if state == data.EngineUnpublished {
		current.State, current.Database, current.Publisher, current.Names = state, "", "", []string{}
	}
	if err = s.CommitEngineRuntime(ctx, current, fence.ID, "prepare-engine"); err != nil {
		t.Fatal(err)
	}
	head, _, err := s.ReadEngineHead(ctx, runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := data.CommittedEngineBase(head)
	if err != nil {
		t.Fatal(err)
	}
	initialAllocation, err := readInitialEngineAllocation(ctx, s.db, reserved)
	if err != nil {
		t.Fatal(err)
	}
	if state == data.EngineUnpublished {
		allocationRaw, _ := json.Marshal(initialAllocation)
		base, err = data.UnpublishedEngineBase(data.EngineUnpublishedBase{Head: head, Allocation: data.EngineEvidenceRef{
			Kind: data.EngineAllocationEvidence, ID: string(reserved.Database.DatabaseID), SHA256: nativeCanonicalHash(allocationRaw),
			DatabaseID: reserved.Database.DatabaseID, BindingID: head.BindingID, EpochID: head.EpochID}})
		if err != nil {
			t.Fatal(err)
		}
	}
	runtime.EpochID = data.ReplicaEpochID(strings.Repeat("e", 32))
	runtime.Names = []string{"recovered"}
	raw, _ := json.Marshal(runtime)
	scope := data.EngineTransitionScope{ID: strings.Repeat("f", 32), ApplyOperationID: "apply-engine", PlanID: "sha256:" + strings.Repeat("1", 64), Requester: "deploy:" + strings.Repeat("2", 64), PolicyHash: "sha256:" + strings.Repeat("3", 64), TargetHash: "sha256:" + strings.Repeat("4", 64), DesiredHash: "sha256:" + strings.Repeat("5", 64), IncarnationID: reserved.Database.IncarnationID, DatabaseID: reserved.Database.DatabaseID, BindingID: runtime.BindingID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	root := data.EngineDirectoryIdentity{Device: 1, Inode: 2}
	parent := data.EngineDirectoryIdentity{Device: 1, Inode: 4}
	stageParent := data.EngineDirectoryIdentity{Device: 1, Inode: 5}
	common := data.EngineTransitionCommon{Engine: data.EngineSpacetimeDB, Scope: scope, Base: base, NewEpochID: runtime.EpochID, FenceID: fence.ID, FenceOwnerOperationID: "prepare-engine",
		PreparedPoint: storeTransitionEvidence(scope, head.EpochID, data.EnginePreparedPointEvidence, 1), SourceWitness: storeTransitionEvidence(scope, head.EpochID, data.EngineSourceWitnessEvidence, 2),
		OldTree:    data.EngineTreeSeal{Root: root, Parent: parent, Directory: data.EngineDirectoryIdentity{Device: 1, Inode: 3}, ContentsSHA256: strings.Repeat("6", 64)},
		TargetTree: data.EngineTreeSeal{Root: root, Parent: stageParent, Directory: data.EngineDirectoryIdentity{Device: 1, Inode: 6}, ContentsSHA256: strings.Repeat("7", 64)}, PreservedParent: stageParent,
		TargetRuntimeSHA256: nativeCanonicalHash(raw), TargetVerification: storeTransitionEvidence(scope, runtime.EpochID, data.EngineTargetEvidence, 3), TargetCheckpoint: storeTransitionEvidence(scope, runtime.EpochID, data.EngineCheckpointEvidence, 4), TargetMaterial: storeTransitionEvidence(scope, runtime.EpochID, data.EngineMaterialEvidence, 5), RecoveryRelease: storeTransitionEvidence(scope, runtime.EpochID, data.EngineRecoveryEvidence, 6), RetainedDependencies: storeTransitionEvidence(scope, runtime.EpochID, data.EngineDependenciesEvidence, 7)}
	input, err := data.NewEnginePublicationTransition(common, data.EnginePublicationTarget{CandidateSHA256: strings.Repeat("8", 64), Receipt: storeTransitionEvidence(scope, runtime.EpochID, data.EnginePublicationEvidence, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO operations(id,requester,idempotency_key,state,created_at,updated_at,kind,app,secret_ref) VALUES(?,?,?,?,?,?,?,?,?)", scope.ApplyOperationID, scope.Requester, "transition-fixture", "preflight", timestamp(), timestamp(), "data_init_apply", "managed", scope.PlanID); err != nil {
		t.Fatal(err)
	}
	preparation, err := s.AcquireEnginePreparation(ctx, runtime.BindingID, fence.ID, "prepare-engine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = preparation.Close() })
	allocation := initialAllocation
	allocation.DirectoryInode = 6
	return nativeTransitionFixture{s, input, preparation, runtime, allocation}
}

func claimTransitionFixture(t *testing.T, f nativeTransitionFixture) data.EngineTransitionCursor {
	t.Helper()
	ctx := context.Background()
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	cursor, err := f.store.insertEngineTransitionTx(ctx, tx, f.preparation, f.input, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return cursor
}

func storeTransitionLocation(t *testing.T, seal data.EngineTreeSeal, parent data.EngineDirectoryIdentity, absent bool) data.EngineLocation {
	t.Helper()
	if absent {
		location, err := data.AbsentEngineLocation(seal.Root, parent)
		if err != nil {
			t.Fatal(err)
		}
		return location
	}
	seal.Parent = parent
	location, err := data.PresentEngineLocation(seal)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func storeTransitionLayout(t *testing.T, input data.EngineTransition, state data.EngineLayoutState) data.EngineLayoutObservation {
	t.Helper()
	c := input.Common()
	live := c.OldTree
	if state == data.EngineLayoutPromoted {
		live = c.TargetTree
	}
	return data.EngineLayoutObservation{
		Live:      storeTransitionLocation(t, live, c.OldTree.Parent, state == data.EngineLayoutPreserved),
		Staged:    storeTransitionLocation(t, c.TargetTree, c.TargetTree.Parent, state == data.EngineLayoutPromoted),
		Preserved: storeTransitionLocation(t, c.OldTree, c.PreservedParent, state == data.EngineLayoutPrepared),
	}
}

func advanceStoreTransition(t *testing.T, f nativeTransitionFixture, cursor data.EngineTransitionCursor, stage data.EngineTransitionStage, layout data.EngineLayoutState) data.EngineTransitionCursor {
	t.Helper()
	next, err := f.store.AdvanceEngineTransition(context.Background(), f.preparation, cursor, stage, storeTransitionLayout(t, f.input, layout))
	if err != nil {
		t.Fatal(stage, err)
	}
	return next
}

func activationCursorFixture(t *testing.T, f nativeTransitionFixture) data.EngineTransitionCursor {
	t.Helper()
	cursor := claimTransitionFixture(t, f)
	cursor = advanceStoreTransition(t, f, cursor, data.EngineTransitionPreserveIntent, data.EngineLayoutPrepared)
	cursor = advanceStoreTransition(t, f, cursor, data.EngineTransitionOriginalPreserved, data.EngineLayoutPreserved)
	cursor = advanceStoreTransition(t, f, cursor, data.EngineTransitionPromoteIntent, data.EngineLayoutPreserved)
	cursor = advanceStoreTransition(t, f, cursor, data.EngineTransitionTargetPromoted, data.EngineLayoutPromoted)
	return advanceStoreTransition(t, f, cursor, data.EngineTransitionActivationIntent, data.EngineLayoutPromoted)
}

func TestNativeActivationAtomicallySelectsNewEpochAllocationAndKeepsOriginal(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	var original []byte
	if err := f.store.db.QueryRowContext(ctx, "SELECT canonical FROM data_allocations WHERE database_id=?", f.input.Common().Scope.DatabaseID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	old := activationCursorFixture(t, f)
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	activated, err := f.store.activateEngineTransitionTx(ctx, tx, f.preparation, old, f.runtime, f.allocation)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	permit, err := f.store.ReadEnginePermit(ctx, f.runtime.BindingID)
	if err != nil || permit.Runtime.EpochID != f.runtime.EpochID || permit.Reserved.Archive.EpochID != f.runtime.EpochID || permit.Allocation.DirectoryInode != 6 || permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("activated permit projected old epoch/inode or released fence", err)
	}
	var retained []byte
	if err = f.store.db.QueryRowContext(ctx, "SELECT canonical FROM data_allocations WHERE database_id=?", f.input.Common().Scope.DatabaseID).Scan(&retained); err != nil || !bytes.Equal(original, retained) {
		t.Fatal("initial allocation changed", err)
	}
	if activated.Stage != data.EngineTransitionActivated {
		t.Fatal("activation cursor not committed")
	}
	finish := advanceStoreTransition(t, f, activated, data.EngineTransitionFinishIntent, data.EngineLayoutPromoted)
	tx, cancel2, err := f.store.beginWrite(ctx)
	defer cancel2()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	proof := storeTransitionEvidence(f.input.Common().Scope, f.runtime.EpochID, data.EngineCompletionEvidence, 9)
	completed, err := f.store.finishEngineTransitionTx(ctx, tx, f.preparation, finish, proof, false)
	if err != nil || tx.Commit() != nil || completed.Stage != data.EngineTransitionCompleted {
		t.Fatal("completion transaction failed", err)
	}
	permit, err = f.store.ReadEnginePermit(ctx, f.runtime.BindingID)
	if err != nil || !permit.AllowsVerification(time.Now().UTC()) {
		t.Fatal("completed target still selected initial data", err)
	}
}
