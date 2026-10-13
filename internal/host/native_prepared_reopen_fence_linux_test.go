//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestPreparedReopenUsesRetainedAllocationFenceOwner(t *testing.T) {
	w, f := stoppedUnpublishedPrepareFixture(t)
	ctx := context.Background()
	point := store.NativePreparedPointView{BindingID: w.input.BindingID, FenceID: w.fence.ID, FenceOwnerOpID: w.fence.OperationID, PreparationOperationID: w.operation.ID}
	if point.FenceOwnerOpID == point.PreparationOperationID {
		t.Fatal("fixture did not separate producer from allocation fence owner")
	}
	before, err := f.service.Store.ReadEnginePermit(ctx, point.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.Store.SetOperationState(ctx, w.operation.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	if err := w.close(ctx); err != nil {
		t.Fatal(err)
	}
	if guard, err := f.service.Store.AcquireEnginePreparation(ctx, point.BindingID, point.FenceID, point.PreparationOperationID); err == nil || guard != nil {
		t.Fatal("fresh succeeded producer borrowed another operation's fence")
	}
	guard, err := w.runtime.acquirePreparedReopenGuard(ctx, point)
	if err != nil || !guard.Active(point.BindingID) {
		t.Fatal("reopen refused the recorded allocation fence owner", err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"owner", "fence", "binding"} {
		foreign := point
		switch kind {
		case "owner":
			foreign.FenceOwnerOpID = point.PreparationOperationID
		case "fence":
			foreign.FenceID = data.FenceID(strings.Repeat("f", 32))
		case "binding":
			foreign.BindingID = data.ReplicaBindingID(strings.Repeat("f", 32))
		}
		if guard, err := w.runtime.acquirePreparedReopenGuard(ctx, foreign); err == nil || guard != nil {
			t.Fatal("foreign point fence acquired a reopen guard", kind)
		}
	}
	after, err := f.service.Store.ReadEnginePermit(ctx, point.BindingID)
	beforeRaw, _ := json.Marshal(before)
	afterRaw, _ := json.Marshal(after)
	if err != nil || !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("reopen changed allocation, runtime or fences", err)
	}
	op, err := f.service.Store.GetOperation(ctx, point.PreparationOperationID)
	if err != nil || op.State != ops.Succeeded {
		t.Fatal("reopen changed the fresh producer", err)
	}
}
