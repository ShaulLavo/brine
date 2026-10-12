//go:build linux

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
)

func TestNativeUnpublishedHeadAdvancesFromActualConfiguredRuntime(t *testing.T) {
	f := nativeTransitionStateFixture(t, data.EngineUnpublished)
	ctx := context.Background()
	before, runtime, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil || runtime.State != data.EngineUnpublished || before.Version != 1 {
		t.Fatal("affirmative unpublished configuration head missing", err)
	}
	activateNativeFixture(t, f)
	after, runtime, err := f.store.ReadEngineHead(ctx, f.runtime.BindingID)
	if err != nil || after.Version != before.Version+1 || runtime.State != data.EngineInitialized || after.EpochID != f.runtime.EpochID {
		t.Fatal("first publication failed to advance configured runtime head", err)
	}
	permit, err := f.store.ReadEnginePermit(ctx, f.runtime.BindingID)
	if err != nil || permit.Runtime.State != data.EngineInitialized || permit.Runtime.EpochID != after.EpochID {
		t.Fatal("published epoch failed current permit join", err)
	}
}

func TestNativeUnpublishedBaseRefusesInitializedRuntimeAndChangedAllocation(t *testing.T) {
	f := nativeTransitionStateFixture(t, data.EngineUnpublished)
	ctx := context.Background()
	reserved, err := f.store.ReadReservedEngine(ctx, f.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	common := f.input.Common()
	unpublished, ok := common.Base.Unpublished()
	if !ok {
		t.Fatal("missing explicit unpublished union arm")
	}
	unpublished.Allocation.SHA256 = strings.Repeat("9", 64)
	common.Base, err = data.UnpublishedEngineBase(unpublished)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := f.input.Publication()
	changed, err := data.NewEnginePublicationTransition(common, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkTransitionBase(ctx, f.store.db, changed, reserved); err == nil {
		t.Fatal("different protected allocation hash admitted unpublished source")
	}
	common = f.input.Common()
	common.Base, err = data.CommittedEngineBase(common.Base.Head())
	if err != nil {
		t.Fatal(err)
	}
	changed, err = data.NewEnginePublicationTransition(common, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkTransitionBase(ctx, f.store.db, changed, reserved); err == nil {
		t.Fatal("configuration-only head admitted initialized history")
	}
	initialized := newNativeTransitionFixture(t)
	reserved, err = initialized.store.ReadReservedEngine(ctx, initialized.runtime.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	common = initialized.input.Common()
	allocation, err := readInitialEngineAllocation(ctx, initialized.store.db, reserved)
	if err != nil {
		t.Fatal(err)
	}
	ref := unpublished.Allocation
	ref.DatabaseID, ref.BindingID, ref.EpochID = reserved.Database.DatabaseID, common.Scope.BindingID, common.Base.Head().EpochID
	ref.ID = string(reserved.Database.DatabaseID)
	allocationRaw, err := json.Marshal(allocation)
	if err != nil {
		t.Fatal(err)
	}
	ref.SHA256 = nativeCanonicalHash(allocationRaw)
	common.Base, err = data.UnpublishedEngineBase(data.EngineUnpublishedBase{Head: common.Base.Head(), Allocation: ref})
	if err != nil {
		t.Fatal(err)
	}
	target, _ = initialized.input.Publication()
	changed, err = data.NewEnginePublicationTransition(common, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkTransitionBase(ctx, initialized.store.db, changed, reserved); err == nil {
		t.Fatal("initialized database admitted unpublished create source")
	}
}
