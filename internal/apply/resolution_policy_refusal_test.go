package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func TestResolutionChangedPolicyNeverAuthorizesRollback(t *testing.T) {
	for _, installed := range []bool{false, true} {
		for _, changed := range []string{"version", "hash"} {
			t.Run(fmt.Sprintf("installed=%v/%s", installed, changed), func(t *testing.T) {
				r, source, successor, _ := healthBoundaryResolution(t)
				configureSettledRecoveryWriter(r)
				r.active = false
				prefix := 6
				if installed {
					prefix = 7
				} else {
					r.setLiveUnits(r.release.Units)
				}
				events := recoveryEvents(forwardSteps[:prefix]...)
				if changed == "version" {
					r.facts.Input.Desired.PolicyVersion = "changed"
				} else {
					r.facts.Input.Desired.PolicyHash = "sha256:" + strings.Repeat("f", 64)
				}
				assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
				if err != nil {
					t.Fatal(err)
				}
				if assessment.Action != RequireRecovery {
					_ = r.executor.Recover(context.Background(), assessment)
					t.Fatalf("changed policy authorized %s with mutations %v", assessment.Action, r.effects)
				}
				if len(r.effects) != 0 {
					t.Fatal("changed policy mutated host", r.effects)
				}
			})
		}
	}
}

func TestResolvePreEffectRefusalAfterJobClears(t *testing.T) {
	r, source, successor, events := healthBoundaryResolution(t)
	configureSettledRecoveryWriter(r)
	r.events = append([]Event(nil), events...)
	pending := false
	system := r.executor.Systemd.(*systemd.Fake)
	system.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return pending, nil }
	r.executor.EffectTimeout = 25 * time.Millisecond
	r.executor.Journal = boundaryRaceJournal{Journal: r, step: "rollback_quiesce", changed: func() { pending = true }}
	assessment, err := r.executor.InspectResolution(context.Background(), successor, source, r.plan, r.desired, events)
	if err != nil || assessment.Action != RestorePrevious {
		t.Fatal(assessment.Action, err)
	}
	_ = r.executor.Recover(context.Background(), assessment)
	if r.state != RecoveryRequired || !pending || !r.active || len(r.effects) != 0 {
		t.Fatal(r.state, pending, r.active, r.effects)
	}
	refusedEvents := append([]Event(nil), r.events...)
	refusedSource := successor
	refusedSource.State = RecoveryRequired
	next := Operation{ID: "next", Kind: ops.Resolve, RecoveryOf: successor.ID, PlanID: r.plan.Hash, State: Queued}
	pending = false
	r.executor.Journal = r
	// Ordinary nonterminal reconciliation must understand the same recorded proof.
	interrupted := next
	interrupted.State = RollingBack
	inspected, err := r.executor.InspectRecovery(context.Background(), interrupted, r.plan, r.desired, refusedEvents)
	if err != nil || inspected.Action != RestorePrevious {
		t.Fatal("reconcile", inspected.Action, err)
	}
	assessment, err = r.executor.InspectResolution(context.Background(), next, refusedSource, r.plan, r.desired, refusedEvents)
	if err != nil || assessment.Action != RestorePrevious {
		t.Fatal("resolution", assessment.Action, err)
	}
	r.state = Queued
	_ = r.executor.Recover(context.Background(), assessment)
	if r.state != RolledBack || !r.active {
		t.Fatal(r.state, r.active, r.effects)
	}
}

func TestResolveRefusalsAtEachRollbackBoundaryAfterJobsClear(t *testing.T) {
	for _, previous := range []bool{false, true} {
		boundaries := []string{"rollback_quiesce", "rollback_unit", "rollback_reload"}
		if previous {
			boundaries = append(boundaries, "rollback_start")
		}
		for _, boundary := range boundaries {
			t.Run(fmt.Sprintf("previous=%v/%s", previous, boundary), func(t *testing.T) {
				r := newRig(t, previous)
				r.failStep = "check_direct"
				r.executor.EffectTimeout = 25 * time.Millisecond
				pending := false
				r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return pending, nil }
				r.executor.Journal = boundaryRaceJournal{Journal: r, step: boundary, changed: func() { pending = true }}
				_ = r.run()
				if r.state != RecoveryRequired || !pending {
					t.Fatal(r.state, pending, r.effects)
				}
				before := append([]Event(nil), r.events...)
				completed := map[string]bool{}
				for _, effect := range r.effects {
					if strings.HasPrefix(effect, "rollback_") {
						completed[effect] = true
					}
				}
				pending = false
				r.failStep = ""
				r.executor.Journal = r
				r.executor.Facts = FactsFunc(func(context.Context) (Facts, error) { return r.facts, nil })
				source := Operation{ID: "operation-1", Kind: ops.Deploy, PlanID: r.plan.Hash, State: RecoveryRequired}
				next := Operation{ID: "next", Kind: ops.Resolve, RecoveryOf: source.ID, PlanID: r.plan.Hash, State: Queued}
				if boundary == "rollback_start" {
					for _, unsafe := range []string{"unknown_restore", "failed_restore", "noncanonical_restore", "missing_compatibility_proof"} {
						altered := append([]Event(nil), before...)
						for i, event := range altered {
							if event.Kind != "step" {
								continue
							}
							var payload ops.StepPayload
							if json.Unmarshal(event.Payload, &payload) != nil {
								t.Fatal("invalid fixture")
							}
							if payload.Step == "rollback_unit" && payload.Outcome == "completed" {
								switch unsafe {
								case "unknown_restore":
									payload.Outcome, payload.Code = "unknown", "interrupted"
								case "failed_restore":
									payload.Outcome, payload.Code = "failed", "rollback_failed"
								case "noncanonical_restore":
									payload.Step = "rollback_reload"
								}
							}
							if unsafe == "missing_compatibility_proof" && payload.Step == "check_compatibility" && payload.Outcome == "completed" {
								payload.Code = ""
							}
							altered[i].Payload, _ = json.Marshal(payload)
						}
						r.effects = nil
						denied, err := r.executor.InspectResolution(context.Background(), next, source, r.plan, r.desired, altered)
						if err != nil || denied.Action != RequireRecovery || len(r.effects) != 0 {
							t.Fatal(unsafe, denied.Action, err, r.effects)
						}
					}
				}
				assessment, err := r.executor.InspectResolution(context.Background(), next, source, r.plan, r.desired, before)
				if err != nil || assessment.Action != RestorePrevious {
					t.Fatal(assessment.Action, err, r.effects)
				}
				r.state = Queued
				r.effects = nil
				_ = r.executor.Recover(context.Background(), assessment)
				if r.state != RolledBack || r.active != previous {
					t.Fatal(r.state, r.active, r.effects)
				}
				for _, effect := range r.effects {
					if completed[effect] {
						t.Fatal("replayed completed rollback effect", effect, r.effects)
					}
				}
			})
		}
	}
}
