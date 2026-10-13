package store

import (
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

func TestPreservedFenceUsesExactSucceededResolverAndOriginalFailure(t *testing.T) {
	scope := datapublish.ProofScope{PlanID: "sha256:" + strings.Repeat("a", 64), ApplyOpID: "original-apply"}
	original := ops.Operation{ID: scope.ApplyOpID, Kind: ops.ModulePublishApply, State: ops.RecoveryRequired, SecretRef: scope.PlanID, App: "game", Requester: "owner"}
	resolver := ops.Operation{ID: "winning-resolver", Kind: ops.ModulePublishResolve, State: ops.Succeeded, RecoveryOf: original.ID, SecretRef: scope.PlanID, App: original.App, Requester: original.Requester}
	record := publicationSourcePreservationRecord{Scope: scope, ResolverID: resolver.ID}
	if !preservedPublicationOperationsMatch(scope, original.App, original.Requester, original, resolver, record) {
		t.Fatal("matching original and succeeded resolver refused")
	}
	for _, state := range []ops.State{ops.Queued, ops.Preflight, ops.Failed, ops.RecoveryRequired} {
		changed := resolver
		changed.State = state
		if preservedPublicationOperationsMatch(scope, original.App, original.Requester, original, changed, record) {
			t.Fatal("unfinished or failed resolver supplied new fence authority", state)
		}
	}
	foreign := resolver
	foreign.ID = "other-resolver"
	if preservedPublicationOperationsMatch(scope, original.App, original.Requester, original, foreign, record) {
		t.Fatal("another succeeded resolver replaced the actual winning proof")
	}
	for _, mutate := range []func(*ops.Operation){
		func(op *ops.Operation) { op.Requester = "foreign" },
		func(op *ops.Operation) { op.App = "foreign" },
		func(op *ops.Operation) { op.SecretRef = "sha256:" + strings.Repeat("b", 64) },
		func(op *ops.Operation) { op.RecoveryOf = "foreign-apply" },
		func(op *ops.Operation) { op.Kind = ops.NativeBackupResolve },
	} {
		changed := resolver
		mutate(&changed)
		if preservedPublicationOperationsMatch(scope, original.App, original.Requester, original, changed, record) {
			t.Fatal("foreign resolver binding supplied authority")
		}
	}
	changedOriginal := original
	changedOriginal.State = ops.Succeeded
	if preservedPublicationOperationsMatch(scope, original.App, original.Requester, changedOriginal, resolver, record) {
		t.Fatal("rewritten original failure supplied authority")
	}
	record.Scope.PointID = "foreign-point"
	if preservedPublicationOperationsMatch(scope, original.App, original.Requester, original, resolver, record) {
		t.Fatal("foreign proof scope supplied authority")
	}
}
