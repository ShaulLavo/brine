package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestQueuedRemoveNoOpRefusesNewPersistentReservation(t *testing.T) {
	r := newRemoveRig(t)
	r.facts.Input.State.Releases = []plan.CurrentRelease{}
	r.facts.Input.Snapshot.Apps = target.Known([]target.App{})
	r.facts.Input.Snapshot.CaddyConfig = target.Known(target.CaddyConfigSet{Files: []target.CaddyFile{}})
	p, err := plan.BuildRemove(r.facts.Input)
	if err != nil || p.Kind != plan.NoOp {
		t.Fatal(p.Kind, err)
	}
	r.plan = p
	r.facts.Input.State.DataIncarnations = []plan.DataIncarnation{{App: p.App, ID: strings.Repeat("a", 32)}}
	if err = r.executor.Run(context.Background(), "operation", p, r.desired); !errors.Is(err, plan.ErrPersistentData) {
		t.Fatalf("queued no-op accepted new persistent ownership: %v", err)
	}
	if len(r.effects) != 0 || r.committed {
		t.Fatalf("refused removal changed the host: %v", r.effects)
	}
}
