//go:build linux

package host

import (
	"testing"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
)

func TestRemovalDispositionRejectsCrossedTaskBeforeEffects(t *testing.T) {
	for _, mode := range []string{"continue_direct", "continue_abandon", "abandon_continue", "retire_recovery", "unknown_task"} {
		t.Run(mode, func(t *testing.T) { checkRemovalDispositionTask(t, mode) })
	}
}

func checkRemovalDispositionTask(t *testing.T, mode string) {
	t.Helper()
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	selectCrossedRemovalTask(t, r, mode)
	var err error
	switch mode {
	case "continue_abandon":
		_, err = r.service.Abandon(t.Context(), r.plan.ArchiveID)
	case "abandon_continue":
		_, err = r.service.Reconcile(t.Context(), r.plan.ArchiveID)
	default:
		_, err = r.service.Retire(t.Context(), r.plan.ArchiveID)
	}
	if err == nil {
		t.Fatal("crossed task admitted", mode)
	}
	d, exists, readErr := r.prep.state.ReadArchiveDisposition(t.Context(), r.plan.ArchiveID)
	if readErr != nil || exists || d.Sequence != 0 {
		t.Fatal("refused task advanced disposition", d, exists, readErr)
	}
	assertRemovalRuntimeUnchanged(t, r)
	if mode == "continue_direct" {
		d, err = r.service.Reconcile(t.Context(), r.plan.ArchiveID)
		if err != nil || d.Stage != archive.Retired {
			t.Fatal("proper resolver continuation failed after direct refusal", d, err)
		}
	}
}

func selectCrossedRemovalTask(t *testing.T, r *removalHostRig, mode string) {
	t.Helper()
	switch mode {
	case "continue_direct", "continue_abandon":
		r.resolve(t, "continue")
	case "abandon_continue":
		r.resolve(t, "abandon")
	case "retire_recovery":
		if err := r.prep.state.MarkSQLiteRetireRecovery(t.Context(), r.task, "interrupted"); err != nil {
			t.Fatal(err)
		}
	case "unknown_task":
		id, err := data.NewID()
		if err != nil {
			t.Fatal(err)
		}
		r.service.Operation = id
	default:
		t.Fatal("unknown test mode", mode)
	}
}

func assertRemovalRuntimeUnchanged(t *testing.T, r *removalHostRig) {
	t.Helper()
	if data.VerifyUnarchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		t.Error("refused task moved the original source")
	}
	if err := r.host.VerifyArchiveRuntime(t.Context(), r.plan); err != nil {
		t.Error("refused task changed owned runtime", err)
	}
	for _, call := range r.recorder.calls {
		if call == "withdraw hello" {
			t.Error("refused task withdrew the route")
		}
	}
	r.noWriterEffects(t)
}
