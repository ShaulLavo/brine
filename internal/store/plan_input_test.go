package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
)

func TestPlanDecisionInputIsDurableAndContentBound(t *testing.T) {
	s := openTest(t)
	in := fixture(t)
	p, err := plan.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.SavePlan(context.Background(), p, in.Desired)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadPlan(context.Background(), id)
	if err != nil || !bytes.Equal(p.DecisionInput(), loaded.DecisionInput()) {
		t.Fatal("lost canonical decision inputs", err)
	}
	if _, err = s.db.Exec("UPDATE plan_inputs SET canonical=? WHERE id=?", []byte("{}"), id); err == nil {
		t.Fatal("mutable decision inputs")
	}
	if _, err = s.db.Exec("DELETE FROM plan_inputs WHERE id=?", id); err == nil {
		t.Fatal("deleted decision inputs")
	}
}
