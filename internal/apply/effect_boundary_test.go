package apply

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type boundaryRaceJournal struct {
	Journal
	step    string
	changed func()
}

func (j boundaryRaceJournal) AppendEvent(ctx context.Context, id string, event Event) (uint64, error) {
	sequence, err := j.Journal.AppendEvent(ctx, id, event)
	var payload ops.StepPayload
	if err == nil && event.Kind == "step" && json.Unmarshal(event.Payload, &payload) == nil && payload.Step == j.step && payload.Outcome == "intent" {
		j.changed()
	}
	return sequence, err
}

func TestRollbackEffectBoundaryRefusesChangedOwnershipOrJob(t *testing.T) {
	for _, resolution := range []bool{false, true} {
		for _, race := range []string{"ownership", "job", "unreadable_job"} {
			for _, boundary := range []string{"rollback_quiesce", "rollback_unit", "rollback_start"} {
				t.Run(map[bool]string{false: "deploy", true: "resolve"}[resolution]+"/"+race+"/"+boundary, func(t *testing.T) {
					var r *rig
					var source, successor Operation
					var events []Event
					if resolution {
						r, source, successor, events = healthBoundaryResolution(t)
						configureSettledRecoveryWriter(r)
					} else {
						r = newRig(t, true)
						configureSettledRecoveryWriter(r)
						r.failStep = "check_direct"
					}
					r.executor.EffectTimeout = 25 * time.Millisecond
					changed := false
					r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) {
						if changed && race == "unreadable_job" {
							return false, injected
						}
						return changed && race == "job", nil
					}
					r.executor.Journal = boundaryRaceJournal{Journal: r.executor.Journal, step: boundary, changed: func() {
						changed = true
						if race == "ownership" {
							(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known([]target.Unit{{Name: r.plan.App + ".container", Hash: "sha256:foreign"}})
						}
					}}
					var err error
					if resolution {
						assessment, e := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
						if e != nil || assessment.Action != RestorePrevious {
							t.Fatalf("assessment=%s err=%v", assessment.Action, e)
						}
						err = r.executor.Recover(context.Background(), assessment)
					} else {
						err = r.run()
					}
					if !changed {
						t.Fatal("effect boundary not reached", err, r.effects)
					}
					for _, effect := range r.effects {
						if effect == boundary {
							t.Fatalf("unsafe effect %s executed after %s changed: effects=%v active=%v state=%s err=%v", boundary, race, r.effects, r.active, r.state, err)
						}
					}
					if hasUnknownEvent(r, boundary) {
						t.Fatal("pre-effect refusal misclassified as unknown mutation")
					}
					if r.state != RecoveryRequired || boundary == "rollback_quiesce" && !r.active {
						t.Fatalf("state=%s active=%v err=%v", r.state, r.active, err)
					}
				})
			}
		}
	}
}

func TestForwardEffectBoundaryRefusesChangedOwnershipOrJob(t *testing.T) {
	for _, race := range []string{"ownership", "job", "unreadable_job"} {
		for _, boundary := range []string{"quiesce_old", "install_unit", "start_unit"} {
			t.Run(race+"/"+boundary, func(t *testing.T) {
				r := newRig(t, true)
				r.executor.EffectTimeout = 25 * time.Millisecond
				changed := false
				r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) {
					if changed && race == "unreadable_job" {
						return false, injected
					}
					return changed && race == "job", nil
				}
				r.executor.Journal = boundaryRaceJournal{Journal: r, step: boundary, changed: func() {
					changed = true
					if race == "ownership" {
						r.setLiveUnits([]target.Unit{{Name: r.plan.App + ".container", Hash: "sha256:foreign"}})
					}
				}}
				err := r.run()
				if !changed || err == nil || r.state != RecoveryRequired {
					t.Fatal(changed, err, r.state)
				}
				for _, effect := range r.effects {
					if effect == boundary {
						t.Fatalf("unsafe forward effect: %v", r.effects)
					}
				}
			})
		}
	}
}
func TestSuccessfulStopWithQueuedJobCannotRestoreOrStart(t *testing.T) {
	r := newRig(t, true)
	r.failStep = "check_direct"
	r.executor.EffectTimeout = 25 * time.Millisecond
	pending := false
	system := r.executor.Systemd.(*systemd.Fake)
	base := system.StopFunc
	system.StopFunc = func(ctx context.Context, unit systemd.Unit) error {
		err := base(ctx, unit)
		if r.intent == "rollback_quiesce" {
			pending = true
		}
		return err
	}
	system.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return pending, nil }
	err := r.run()
	if err == nil || r.state != RecoveryRequired || r.active {
		t.Fatal(err, r.state, r.active)
	}
	for _, effect := range r.effects {
		if effect == "rollback_unit" || effect == "rollback_start" {
			t.Fatalf("mutation after stop left a queued job: %v", r.effects)
		}
	}
}

func TestFirstDeployRollbackCannotStopAfterCandidateDisappears(t *testing.T) {
	r := newRig(t, false)
	r.failStep = "check_direct"
	r.executor.Journal = boundaryRaceJournal{Journal: r, step: "rollback_quiesce", changed: func() { r.setLiveUnits([]target.Unit{}) }}
	err := r.run()
	if err == nil || r.state != RecoveryRequired || !r.active {
		t.Fatal(err, r.state, r.active, r.effects)
	}
	for _, effect := range r.effects {
		if effect == "rollback_quiesce" {
			t.Fatal("stopped without a live owned artifact", r.effects)
		}
	}
}
