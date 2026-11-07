//go:build linux

package host

import (
	"github.com/ShaulLavo/brine/internal/data"
	"testing"
)

func TestRemovalAbandonResolverCannotUseDirectRetirement(t *testing.T) {
	r := newRemovalHostRig(t)
	if _, err := r.service.Prepare(t.Context(), "hello", r.plan.ID); err != nil {
		t.Fatal(err)
	}
	r.resolve(t, "abandon")
	_, err := r.service.Retire(t.Context(), r.plan.ArchiveID)
	t.Logf("direct retirement with genuine abandon resolver returned %v", err)
	if err == nil {
		t.Fatal("abandon resolver admitted as direct retirement")
	}
	if data.VerifyUnarchivedTree(r.plan.Trees[0], r.plan.Candidate.Incarnation, r.plan.ArchiveID) != nil {
		t.Error("abandon resolver moved the original source before final task refusal")
	}
	for _, call := range r.recorder.calls {
		if call == "withdraw hello" {
			t.Error("abandon resolver withdrew the route before final task refusal")
		}
	}
}
