//go:build linux

package store

import (
	"context"
	"reflect"
	"testing"
)

func TestNativeArchiveRetainsInitialPendingAndActivatedEpochs(t *testing.T) {
	f := newNativeTransitionFixture(t)
	ctx := context.Background()
	initial, err := f.store.ReadReservedEngine(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.store.ReadEngineArchiveBindings(ctx, f.runtime.BindingID)
	if err != nil || len(before) != 1 || before[0] != initial.Archive {
		t.Fatal("initial backup epoch omitted", err)
	}
	claimTransitionFixture(t, f)
	pending, err := f.store.ReadEngineArchiveBindings(ctx, f.runtime.BindingID)
	if err != nil || len(pending) != 2 || pending[0] != initial.Archive || pending[1].EpochID != f.runtime.EpochID {
		t.Fatal("unfinished reserved epoch was not retained", err)
	}
	activateNativeFixture(t, f)
	activated, err := f.store.ReadEngineArchiveBindings(ctx, f.runtime.BindingID)
	if err != nil || !reflect.DeepEqual(activated, pending) {
		t.Fatal("activation discarded previous recovery destination", err)
	}
	activated[0].RemotePrefix = "caller-changed"
	actual, err := f.store.ReadEngineArchiveBindings(ctx, f.runtime.BindingID)
	if err != nil || !reflect.DeepEqual(actual, pending) {
		t.Fatal("caller changed retained archive references", err)
	}
}
