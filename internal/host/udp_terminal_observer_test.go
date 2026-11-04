//go:build linux

package host

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
)

func TestTerminalResolverCannotCollectOwnedUDPSocket(t *testing.T) {
	h := newUDPRuntime(t)
	h.create(t)
	p := h.lifecycle(t, plan.RemoveApp)
	accepted := h.call(t, "apply", dispatch.ApplyArgs{PlanID: p.PlanID, IdempotencyKey: "terminal-remove"}).Data.(jobs.Accepted)
	if err := h.store.SetOperationState(t.Context(), accepted.OperationID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	resolver, _, err := h.store.CreateOperation(t.Context(), ops.Intent{Kind: ops.Resolve, RecoveryOf: accepted.OperationID, PlanID: p.PlanID}, "udp-test", "terminal-resolver")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetOperationState(t.Context(), resolver.ID, ops.Failed); err != nil {
		t.Fatal(err)
	}
	ordinary, err := h.collector.Collect(t.Context())
	if err != nil || ordinary.UDPSockets.Value == nil {
		t.Fatal("ordinary read", err)
	}
	for _, socket := range *ordinary.UDPSockets.Value {
		if socket.App == "hello" {
			t.Fatal("ordinary read unexpectedly owns the socket")
		}
	}
	bound, err := h.collector.CollectFor(t.Context(), resolver.ID, p.PlanID)
	if err != nil {
		return
	}
	if bound.UDPSockets.Value == nil {
		t.Fatal("terminal observer accepted with unknown socket evidence")
	}
	for _, socket := range *bound.UDPSockets.Value {
		if socket.App == "hello" {
			t.Fatal("failed resolver makes the unresolved removal's socket owned through the production collector")
		}
	}
	t.Fatal("terminal observer accepted instead of refused")
}
