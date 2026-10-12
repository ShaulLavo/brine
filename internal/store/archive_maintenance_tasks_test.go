package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/ops"
)

func TestArchiveMaintenanceTaskRegistrationPreservesCurrentOperations(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	requester := "deploy:" + strings.Repeat("a", 64)
	planID := "sha256:" + strings.Repeat("b", 64)
	first, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.ArchiveMaintenanceApply, App: "example", SecretRef: planID}, requester, "maintenance-key")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Kind.IsTask() {
		t.Fatal("maintenance became synchronous task")
	}
	replay, existing, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.ArchiveMaintenanceApply, App: "example", SecretRef: planID}, requester, "maintenance-key")
	if err != nil || !existing || replay.ID != first.ID {
		t.Fatal("immutable task key changed", err)
	}
	if _, _, err = s.CreateOperation(ctx, ops.Intent{Kind: ops.ArchiveMaintenanceApply, App: "example", SecretRef: planID}, requester, "different-key"); err == nil {
		t.Fatal("reviewed effect got a second attempt")
	}
	for _, state := range []ops.State{ops.Preflight, ops.RecoveryRequired} {
		if err = s.SetOperationState(ctx, first.ID, state); err != nil {
			t.Fatal(err)
		}
	}
	resolved, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.ArchiveMaintenanceResolve, App: "example", SecretRef: planID, RecoveryOf: first.ID}, requester, "resolve-key")
	if err != nil || resolved.RecoveryOf != first.ID {
		t.Fatal("typed original resolution unavailable", err)
	}
	if _, _, err = s.CreateOperation(ctx, ops.Intent{Kind: ops.NativePrepare, App: "example", SecretRef: "sha256:" + strings.Repeat("c", 64)}, requester, "previous-kind"); err != nil {
		t.Fatal("existing24 task kind lost", err)
	}
}
