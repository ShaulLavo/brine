//go:build linux

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestNativeRecoveryReservationsPreserveVersionAndIdempotentScope(t *testing.T) {
	state, plan, _ := managedControlFixture(t)
	ctx := context.Background()
	requester := "deploy:" + strings.Repeat("c", 64)
	key, hash := strings.Repeat("d", 32), strings.Repeat("e", 64)
	first, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, key, hash)
	if err != nil || first.Version != 1 {
		t.Fatal("initial independent recovery reservation", err)
	}
	replay, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, key, hash)
	if err != nil || replay != first {
		t.Fatal("lost-response retry changed private version", err)
	}
	if _, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, key, strings.Repeat("f", 64)); err == nil {
		t.Fatal("same delivery key selected different material")
	}
	next, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, strings.Repeat("a", 32), strings.Repeat("f", 64))
	if err != nil || next.Version != 2 {
		t.Fatal("partial reserved recovery version reused", err)
	}
}

func TestNativeRecoveryDeliveryRequiresOriginalTaskAndRealLaunchGuard(t *testing.T) {
	state, plan, _ := managedControlFixture(t)
	ctx := context.Background()
	requester := "deploy:" + strings.Repeat("c", 64)
	input, err := state.ReserveNativeRecoveryInput(ctx, plan.Hash, requester, strings.Repeat("d", 32), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := state.ClaimNativeRecoveryDelivery(ctx, input.ID, requester)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = delivery.Close() }()
	op, _, err := state.CreateOperation(ctx, ops.Intent{Kind: ops.NativeRecoverySet, App: input.App, SecretRef: input.ID}, requester, input.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordNativeRecoveryDelivery(ctx, delivery, op.ID); err == nil {
		t.Fatal("queued task recorded private file delivery")
	}
	if err := state.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	receipt, err := state.RecordNativeRecoveryDelivery(ctx, delivery, op.ID)
	if err != nil || receipt.Version != input.Version || receipt.Reference != input.Reference {
		t.Fatal("protected delivery receipt", err)
	}
	if err := delivery.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.Input(ctx); err == nil {
		t.Fatal("released launch guard retained delivery authority")
	}
	if _, err := state.RecordNativeRecoveryDelivery(ctx, delivery, op.ID); err == nil {
		t.Fatal("released guard rewrote delivery receipt")
	}
}

func TestNativeRecoveryPasswordCannotReuseAnEngineRoleValue(t *testing.T) {
	state, plan, _ := managedControlFixture(t)
	_, runtime, err := state.ReadEngineHead(context.Background(), plan.Managed.Database.BindingID)
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{runtime.Secrets.Publisher.SHA256, runtime.Secrets.SigningPrivate.SHA256, runtime.Secrets.SigningPublic.SHA256, runtime.Secrets.AuthPolicy.SHA256} {
		if _, err := state.ReserveNativeRecoveryInput(context.Background(), plan.Hash, "deploy:"+strings.Repeat("c", 64), strings.Repeat("d", 32), hash); err == nil {
			t.Fatal("independent recovery password reused an engine role value")
		}
	}
}
