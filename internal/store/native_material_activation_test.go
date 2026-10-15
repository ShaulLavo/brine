//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativematerial"
)

func TestMaterialRevisionPreservesRestoredEpochAllocation(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := t.Context()
	cursor := activationCursorFixture(t, f)
	tx, cancel, err := f.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	activated, err := f.store.activateEngineTransitionTx(ctx, tx, f.preparation, cursor, f.runtime, f.allocation)
	if err != nil || tx.Commit() != nil {
		t.Fatal("transition activation", err)
	}
	finish := advanceStoreTransition(t, f, activated, data.EngineTransitionFinishIntent, data.EngineLayoutPromoted)
	tx, cancel2, err := f.store.beginWrite(ctx)
	defer cancel2()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	proof := storeTransitionEvidence(f.input.Common().Scope, f.runtime.EpochID, data.EngineCompletionEvidence, 9)
	if _, err = f.store.finishEngineTransitionTx(ctx, tx, f.preparation, finish, proof, false); err != nil || tx.Commit() != nil {
		t.Fatal("transition completion", err)
	}
	head, runtime, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Secrets = materialVersions(runtime.Secrets, 2)
	raw, _ := json.Marshal(runtime)
	now := time.Now().UTC()
	id, err := data.NewID()
	if err != nil {
		t.Fatal(err)
	}
	p := nativematerial.ActivationPlan{ID: id, App: "game", Requester: "fixture", DeliveryOperationID: "material-delivery", Head: head, DesiredRevision: 1, PolicyHash: f.input.Common().Scope.PolicyHash, Secrets: runtime.Secrets, RestartRequired: true, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	planRaw, _ := json.Marshal(p)
	receipt := nativematerial.ActivationReceipt{PlanID: id, BindingID: head.BindingID, EpochID: head.EpochID, RuntimeVersion: head.Version + 1, RuntimeSHA256: nativeCanonicalHash(raw)}
	receiptRaw, _ := json.Marshal(receipt)
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_native_material_activation_plans VALUES(?,?)", id, planRaw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_engine_runtime_commits VALUES(?,?,?)", head.BindingID, receipt.RuntimeVersion, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_native_material_activations VALUES(?,?,?,?)", id, head.BindingID, receipt.RuntimeVersion, receiptRaw); err != nil {
		t.Fatal(err)
	}
	permit, err := f.store.ReadEnginePermit(ctx, head.BindingID)
	if err != nil || permit.Runtime.Secrets != runtime.Secrets || permit.Allocation.DirectoryInode != f.allocation.DirectoryInode || permit.Reserved.Archive.EpochID != head.EpochID {
		t.Fatal("material selection lost restored allocation", err)
	}
	var historical []byte
	if err = f.store.db.QueryRowContext(ctx, "SELECT canonical FROM data_engine_runtime_commits WHERE binding_id=? AND version=?", head.BindingID, head.Version).Scan(&historical); err != nil || nativeCanonicalHash(historical) != head.RuntimeSHA256 {
		t.Fatal("historical signing runtime changed", err)
	}
}

func dropNativeMaterialActivationMigrationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	for _, table := range []string{"data_native_material_activations", "data_native_material_activation_plans"} {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
			t.Fatal(err)
		}
	}
}
