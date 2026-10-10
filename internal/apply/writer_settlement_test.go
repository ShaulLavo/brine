package apply

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/target"
)

// Model the store's uncleared-intent exclusion: another operation cannot bind
// until the old owner is explicitly cleared, even if that owner's receipt died.
type pendingWriterStarts struct {
	writerStartsFake
	pending   map[string]bool
	failOwner string
}

func (f *pendingWriterStarts) BindWriterStart(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
	for owner := range f.pending {
		if owner != id {
			return errors.New("uncleared writer intent")
		}
	}
	if err := f.writerStartsFake.BindWriterStart(ctx, id, p, d); err != nil {
		return err
	}
	f.pending[id] = true
	return nil
}
func (f *pendingWriterStarts) ClearWriterStart(ctx context.Context, id string) error {
	if err := f.writerStartsFake.ClearWriterStart(ctx, id); err != nil {
		return err
	}
	if id == f.failOwner {
		return errors.New("clear outcome unknown")
	}
	delete(f.pending, id)
	return nil
}

func committedPersistentRecovery(t *testing.T, owner string) *rig {
	t.Helper()
	r := persistentRecoveryRig(t)
	configureSettledRecoveryWriter(r)
	unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	r.release = Release{ID: owner, PlanID: r.plan.Hash, Image: r.plan.Image, HostPort: r.plan.HostPort, Secrets: r.plan.Secrets, Units: []target.Unit{{Name: unit.Name(), Hash: unit.Hash()}}, CaddyFile: target.CaddyFile{Name: r.plan.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, CaddyGeneration: 1}
	r.hasRelease = true
	observeCommittedRelease(r)
	return r
}

func TestPersistentWriterSettlementAfterCommittedRecovery(t *testing.T) {
	for _, path := range []string{"reconcile", "resolve", "repeated_resolve"} {
		t.Run(path, func(t *testing.T) {
			owner := "operation-1"
			r := committedPersistentRecovery(t, owner)
			intent := &pendingWriterStarts{pending: map[string]bool{owner: true}}
			r.executor.WriterStarts = intent
			events := recoveryEvents(deploymentSteps(r.desired)...)
			var assessment Recovery
			var err error
			switch path {
			case "reconcile":
				r.state = Committing
				assessment, err = r.executor.InspectRecovery(context.Background(), Operation{ID: owner, Kind: ops.Deploy, PlanID: r.plan.Hash, State: Committing}, r.plan, r.desired, events)
			case "resolve":
				r.state = Queued
				source := Operation{ID: owner, Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
				successor := Operation{ID: "successor", Kind: ops.Resolve, RecoveryOf: owner, PlanID: r.plan.Hash, State: Queued}
				assessment, err = r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			default:
				r.state = Queued
				source, successor := repeatedResolution(r, owner)
				intent.pending["middle"] = true
				intent.pending["latest"] = true
				assessment, err = r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
			}
			if err != nil || assessment.Action != FinishSucceeded {
				t.Fatal("commit not independently proved", assessment, err)
			}
			if err := r.executor.Recover(context.Background(), assessment); err != nil || r.state != Succeeded {
				t.Fatal(err, r.state)
			}
			if err := intent.BindWriterStart(context.Background(), "next-deploy", r.plan, r.desired); err != nil {
				t.Fatal("settled recovery left app wedged", err, intent.calls, intent.pending)
			}
		})
	}
}

func TestPersistentResolutionIntentCleanupOnlyAtSettledTerminal(t *testing.T) {
	for _, state := range []State{Succeeded, Failed, RolledBack, RecoveryRequired} {
		t.Run(string(state), func(t *testing.T) {
			x, _, _ := writerExecution(t)
			x.writerStartOwners = []string{"source", "ancestor"}
			intent := &pendingWriterStarts{pending: map[string]bool{"operation": true, "source": true, "ancestor": true}}
			x.executor.WriterStarts = intent
			journal := x.executor.Journal.(*rig)
			switch state {
			case Succeeded:
				journal.state = Committing
			case RolledBack:
				journal.state = RollingBack
			}
			if err := x.terminal(context.Background(), state, nil); err != nil {
				t.Fatal(err)
			}
			if state == RecoveryRequired {
				if len(intent.calls) != 0 || len(intent.pending) != 3 {
					t.Fatal("unknown settlement destroyed evidence", intent)
				}
			} else if len(intent.pending) != 0 {
				t.Fatal("settled source intent remains", intent)
			}
		})
	}
}

func TestPersistentResolutionSourceCleanupUnknownRefusesSettlement(t *testing.T) {
	r := committedPersistentRecovery(t, "source")
	r.state = Queued
	intent := &pendingWriterStarts{pending: map[string]bool{"source": true}, failOwner: "source"}
	r.executor.WriterStarts = intent
	source := Operation{ID: "source", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
	successor := Operation{ID: "successor", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
	assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, recoveryEvents(deploymentSteps(r.desired)...))
	if err != nil || assessment.Action != FinishSucceeded {
		t.Fatal(assessment, err)
	}
	if err := r.executor.Recover(context.Background(), assessment); err == nil || r.state != RecoveryRequired || !intent.pending[source.ID] {
		t.Fatal("unknown source clear reported settled", err, r.state, intent)
	}
}

func TestPersistentResolutionClearsSourceBeforeUnitCompensation(t *testing.T) {
	r := newRig(t, true)
	configureSettledRecoveryWriter(r)
	r.active = false
	r.state = Starting
	unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	// Even affirmative read-back of an already-restored unit is not proof
	// that the source writer intent was cleared.
	r.setLiveUnits(r.release.Units)
	r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
	d := r.desired
	d.Databases = []data.Database{{Name: "main"}}
	intent := &pendingWriterStarts{pending: map[string]bool{"source": true}, failOwner: "source"}
	r.executor.WriterStarts = intent
	x := &execution{executor: &r.executor, id: "operation", writerStartOwners: []string{"source"}, plan: r.plan, desired: d, previous: r.release, previousDesired: r.oldDesired, hasPrevious: true, unit: unit, service: mustUnit(t), facts: r.facts, installed: true, quiesced: true, state: Starting}
	if err := x.fail(context.Background(), &Error{Step: "start_unit", Code: "writer_permit_refused"}); err == nil || r.state != RecoveryRequired {
		t.Fatal(err, r.state)
	}
	for _, effect := range r.effects {
		if effect == "rollback_unit" || effect == "rollback_start" {
			t.Fatal("unit compensated before source clear", r.effects)
		}
	}
	if !intent.pending["source"] {
		t.Fatal("unknown source cleanup lost evidence")
	}
}
