package host

import (
	"context"
	"testing"

	"github.com/ShaulLavo/brine/internal/spec"
)

func TestPlanFailsClosed(t *testing.T) {
	for _, s := range []Service{{}, {Requester: "deploy"}} {
		if _, err := s.Plan(context.Background(), spec.App{}); err == nil {
			t.Fatal("unwired planning succeeded")
		}
	}
}
