package apply

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

// Preserve the review overlay: accepted stop, still active/running, no proof
// that the manager job settled. A rollback start would race that queued stop.
func TestUnknownStopRunningWithoutJobEvidenceNeverRestartsReviewerCase(t *testing.T) {
	r := newRig(t, true)
	r.unknownStep = "quiesce_old"
	r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
	}
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{Running: true, Status: "running"}, nil
	}
	r.executor.Systemd.(*systemd.Fake).StartFunc = func(context.Context, systemd.Unit) error {
		t.Fatalf("further mutation %s while stop job remains indeterminate; effects=%v", r.intent, r.effects)
		return nil
	}
	failure(t, r.run(), RecoveryRequired, "quiesce_old")
	if r.effects[len(r.effects)-1] != "quiesce_old" {
		t.Fatal(r.effects)
	}
}

func TestUnknownStartRunningWithoutJobEvidenceCannotContinue(t *testing.T) {
	r := newRig(t, true)
	r.unknownStep = "start_unit"
	r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
	}
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{Running: r.active, Status: map[bool]string{true: "running", false: "exited"}[r.active]}, nil
	}
	r.executor.Health = healthFunc(func(context.Context, policy.Desired, target.Port, bool) error {
		t.Fatal("health checked before the start job was known to settle")
		return nil
	})
	failure(t, r.run(), RecoveryRequired, "start_unit")
	if r.effects[len(r.effects)-1] != "start_unit" {
		t.Fatal(r.effects)
	}
}

func TestUnknownServiceActionWaitsForItsManagerJob(t *testing.T) {
	for _, step := range []string{"quiesce_old", "rollback_quiesce", "start_unit", "rollback_start"} {
		for _, settles := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/settles=%v", step, settles), func(t *testing.T) {
				r := newRig(t, true)
				r.unknownStep = step
				r.executor.EffectTimeout = 200 * time.Millisecond
				if strings.HasPrefix(step, "rollback_") {
					r.failStep = "check_direct"
				}
				stoppedOrRunningProbes(r)
				unknownPending := false
				queries := 0
				system := r.executor.Systemd.(*systemd.Fake)
				system.JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) {
					if !hasUnknownEvent(r, step) {
						return false, nil
					}
					queries++
					if settles && queries >= 5 {
						unknownPending = false
						if strings.Contains(step, "quiesce") {
							r.active = false
						} else {
							r.active = true
						}
					}
					return unknownPending, nil
				}
				system.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
					if !r.active {
						return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
					}
					return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
				}
				baseStop, baseStart := system.StopFunc, system.StartFunc
				system.StopFunc = func(ctx context.Context, unit systemd.Unit) error {
					if unknownPending {
						t.Fatalf("mutation %s while prior manager job is pending", r.intent)
					}
					err := baseStop(ctx, unit)
					if r.intent == step {
						unknownPending = true
					}
					return err
				}
				system.StartFunc = func(ctx context.Context, unit systemd.Unit) error {
					if unknownPending {
						t.Fatalf("mutation %s while prior manager job is pending", r.intent)
					}
					err := baseStart(ctx, unit)
					if r.intent == step {
						unknownPending = true
					}
					return err
				}
				baseHealth := r.executor.Health
				r.executor.Health = healthFunc(func(ctx context.Context, d policy.Desired, port target.Port, routed bool) error {
					if unknownPending {
						t.Fatal("health probe while manager job is pending")
					}
					if r.intent == step {
						return nil
					}
					return baseHealth.Check(ctx, d, port, routed)
				})
				err := r.run()
				if !settles {
					failure(t, err, RecoveryRequired, step)
					if r.effects[len(r.effects)-1] != step {
						t.Fatal(r.effects)
					}
				} else if strings.HasPrefix(step, "rollback_") {
					failure(t, err, RolledBack, "check_direct")
				} else if err != nil {
					t.Fatal(err)
				}
				if queries < 4 {
					t.Fatalf("job was not re-inspected: %d reads", queries)
				}
			})
		}
	}
}

func TestUnitTransitionWaitsEvenWhenJobQueueIsEmpty(t *testing.T) {
	for _, active := range []string{"activating", "deactivating"} {
		t.Run(active, func(t *testing.T) {
			r := newRig(t, true)
			stoppedOrRunningProbes(r)
			reads := 0
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				reads++
				if reads == 1 {
					return systemd.Properties{ActiveState: active, SubState: map[string]string{"activating": "start", "deactivating": "stop"}[active]}, nil
				}
				return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
			}
			r.active = false
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			x := execution{executor: &r.executor, plan: r.plan}
			if got := x.waitWriter(ctx); got != writerStopped || reads < 2 {
				t.Fatalf("writer=%v reads=%d", got, reads)
			}
		})
	}
}

func TestJobQueuedBetweenStateProbesCannotLookSettled(t *testing.T) {
	r := newRig(t, true)
	stoppedOrRunningProbes(r)
	reads := 0
	r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { reads++; return reads == 2, nil }
	x := execution{executor: &r.executor, plan: r.plan}
	if got := x.inspectWriter(context.Background()); got != writerPending {
		t.Fatalf("accepted stale active/running snapshot: %v", got)
	}
}

// Job observes every manager job on the unit, including a restart accepted
// while the unknown-start direct-health probe was in flight.
func TestPendingRestartAfterUnknownStartHealthCannotContinue(t *testing.T) {
	r := newRig(t, true)
	r.unknownStep = "start_unit"
	r.executor.EffectTimeout = 200 * time.Millisecond
	stoppedOrRunningProbes(r)
	restartPending := false
	r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) { return restartPending, nil }
	r.executor.Health = healthFunc(func(context.Context, policy.Desired, target.Port, bool) error { restartPending = true; return nil })
	failure(t, r.run(), RecoveryRequired, "start_unit")
	if r.effects[len(r.effects)-1] != "start_unit" {
		t.Fatalf("mutation while restart remained queued: %v", r.effects)
	}
}

func TestFailedJobReadBeforeOrAfterSnapshotNeverMeansSettled(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			r := newRig(t, true)
			r.unknownStep = "start_unit"
			stoppedOrRunningProbes(r)
			reads := 0
			r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) {
				reads++
				if reads == failAt {
					return false, injected
				}
				return false, nil
			}
			r.executor.Health = healthFunc(func(context.Context, policy.Desired, target.Port, bool) error {
				t.Fatal("health probe without settled job evidence")
				return nil
			})
			failure(t, r.run(), RecoveryRequired, "start_unit")
			if r.effects[len(r.effects)-1] != "start_unit" {
				t.Fatal(r.effects)
			}
		})
	}
}

func TestKnownPendingJobStillWaitsWhenTheSecondQueueReadFails(t *testing.T) {
	r := newRig(t, true)
	r.unknownStep = "quiesce_old"
	stoppedOrRunningProbes(r)
	reads := 0
	r.executor.Systemd.(*systemd.Fake).JobPendingFunc = func(context.Context, systemd.Unit) (bool, error) {
		reads++
		switch reads {
		case 1:
			return true, nil
		case 2:
			return false, injected
		default:
			r.active = false
			return false, nil
		}
	}
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if reads < 4 {
		t.Fatal("accepted incomplete pending-job evidence")
	}
}
