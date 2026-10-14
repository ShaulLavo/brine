//go:build linux

package host

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/store"
)

func TestNativeAbandonDetachedTaskUsesProtectedRequester(t *testing.T) {
	rig, planned, _, _ := managedFactsRecoveryFixture(t)
	requester, err := datainit.ParseRequester("deploy:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := rig.store.SaveNativeTaskInput(t.Context(), store.NativeTaskInput{Requester: requester, Kind: ops.NativePrepareAbandon, BindingID: planned.Managed.Database.BindingID, PointID: strings.Repeat("b", 32)})
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := rig.store.CreateOperation(t.Context(), ops.Intent{Kind: ops.NativePrepareAbandon, App: planned.App, SecretRef: reference}, requester.String(), "detached-abandon-principal")
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.store.SetOperationState(t.Context(), op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	op.State = ops.Preflight
	base := Service{Store: rig.store, Policy: rig.policy}
	scoped, err := nativeAbandonTaskService(t.Context(), base, op)
	if err != nil || scoped.Requester != requester.String() || base.Requester != "" {
		t.Fatal("detached requester was not scoped from accepted input", err)
	}
	lock, err := rig.store.AcquireHostLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	wait, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	receipt, err := nativeAbandonTask(base, nativeBackupRuntime{state: rig.store})(wait, op)
	if !errors.Is(err, context.DeadlineExceeded) || len(receipt) != 0 {
		t.Fatal("detached handler refused before protected host lock", err)
	}
	current, err := rig.store.GetOperation(t.Context(), op.ID)
	if err != nil || current.State != ops.Preflight {
		t.Fatal("host lock wait changed task", err)
	}
	outcome, err := rig.store.ReadTaskOutcome(t.Context(), op.ID)
	if err != nil || outcome != nil {
		t.Fatal("host lock wait committed an outcome", err)
	}
	for _, field := range []string{"requester", "reference", "app", "kind", "state"} {
		t.Run(field, func(t *testing.T) { assertNativeAbandonForeignTask(t, base, op, field) })
	}
}

func assertNativeAbandonForeignTask(t *testing.T, service Service, op ops.Operation, field string) {
	t.Helper()
	switch field {
	case "requester":
		op.Requester = "deploy:" + strings.Repeat("f", 64)
	case "reference":
		op.SecretRef = strings.Repeat("f", 64)
	case "app":
		op.App = "foreign"
	case "kind":
		op.Kind = ops.NativePrepare
	case "state":
		op.State = ops.Queued
	}
	if _, err := nativeAbandonTaskService(t.Context(), service, op); err == nil {
		t.Fatal("foreign task adopted requester")
	}
}
