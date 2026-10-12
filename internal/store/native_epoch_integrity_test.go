//go:build linux

package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func activateNativeFixture(t *testing.T, fixture nativeTransitionFixture) data.EngineTransitionCursor {
	t.Helper()
	ctx := context.Background()
	old := activationCursorFixture(t, fixture)
	tx, cancel, err := fixture.store.beginWrite(ctx)
	defer cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	cursor, err := fixture.store.activateEngineTransitionTx(ctx, tx, fixture.preparation, old, fixture.runtime, fixture.allocation)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return cursor
}

func TestNativePermitRejectsPhantomActivationVersion(t *testing.T) {
	f := newNativeTransitionFixture(t)
	activateNativeFixture(t, f)
	ctx := context.Background()
	head, runtime, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = f.store.db.QueryRowContext(ctx, "SELECT canonical FROM data_native_epoch_materials WHERE binding_id=? AND epoch_id=? AND runtime_version=?", head.BindingID, head.EpochID, head.Version).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var material nativeEpochMaterial
	if err = json.Unmarshal(raw, &material); err != nil {
		t.Fatal(err)
	}
	material.RuntimeVersion = head.Version + 1
	materialRaw, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRaw, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_engine_runtime_commits VALUES(?,?,?)", head.BindingID, material.RuntimeVersion, runtimeRaw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(ctx, "INSERT INTO data_native_epoch_materials VALUES(?,?,?,?)", head.BindingID, head.EpochID, material.RuntimeVersion, materialRaw); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ReadEnginePermit(ctx, head.BindingID); err == nil {
		t.Fatal("runtime version without matching activation event admitted startup")
	}
}

func TestNativeInitialRuntimeCommitCannotRewindActivatedEpoch(t *testing.T) {
	f := newNativeTransitionFixture(t)
	activateNativeFixture(t, f)
	if err := f.preparation.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	initial, err := f.store.ReadReservedEngine(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	common := f.input.Common()
	if err = f.store.CommitEngineRuntime(ctx, engineRuntimeFixture(initial), common.FenceID, common.FenceOwnerOperationID); err == nil {
		t.Fatal("initial commit API rewound a successor epoch")
	}
	permit, err := f.store.ReadEnginePermit(ctx, f.runtime.BindingID)
	if err != nil || permit.Runtime.EpochID != f.runtime.EpochID || permit.Allocation.DirectoryInode != f.allocation.DirectoryInode {
		t.Fatal("refused rewind changed active epoch/allocation", err)
	}
}
