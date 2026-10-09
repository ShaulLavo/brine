package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func TestTypedUnknownOutcomesAtEveryMutationBoundary(t *testing.T) {
	steps := []string{"pull_image", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "publish_route", "commit", "rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_route", "rollback_start"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			r.state = Starting
			r.executor.Podman.(*podman.Fake).InspectFunc = func(context.Context, podman.Image) (podman.ImageInfo, error) {
				return podman.ImageInfo{}, &localexec.Error{Kind: localexec.UnknownOutcome}
			}
			x := &execution{executor: &r.executor, id: "operation-1", state: Starting, plan: r.plan, desired: r.desired, previous: r.release, previousDesired: r.oldDesired, hasPrevious: true}
			err := x.step(context.Background(), step, Starting, "unit_failed", func(context.Context) error {
				return fmt.Errorf("wrapped: %w", &localexec.Error{Kind: localexec.UnknownOutcome, ExitCode: -1})
			})
			var classified *Error
			if !errors.As(err, &classified) || classified.Code != "interrupted" {
				t.Fatalf("indeterminate mutation classified as %v", err)
			}
			if !hasUnknownEvent(r, step) {
				t.Fatal("unknown outcome not journaled")
			}
		})
	}
}

func TestTypedAdapterUnknownClassification(t *testing.T) {
	for _, err := range []error{&localexec.Error{Kind: localexec.UnknownOutcome}, &localexec.Error{Kind: localexec.Timeout}, &caddy.UnknownOutcomeError{Stage: "reload"}, quadlet.ErrPublicationUnknown} {
		if !isUnknown(fmt.Errorf("wrapped: %w", err)) {
			t.Errorf("not unknown: %T", err)
		}
	}
	if isUnknown(&localexec.Error{Kind: localexec.Failed}) {
		t.Fatal("known failure classified as unknown")
	}
}

func TestUnknownStartInspectsBeforeAnyFurtherMutation(t *testing.T) {
	for _, tc := range []struct {
		name, unit, container string
		healthy               bool
		want                  State
	}{
		{"running healthy", "active", "running", true, Succeeded},
		{"not running", "inactive", "exited", false, RolledBack},
		{"still activating", "activating", "running", true, RecoveryRequired},
		{"unit container disagree", "active", "exited", true, RecoveryRequired},
		{"running unhealthy", "active", "running", false, RecoveryRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, true)
			inspected := false
			startCalls := 0
			system := r.executor.Systemd.(*systemd.Fake)
			system.StartFunc = func(context.Context, systemd.Unit) error {
				if err := r.hit(r.intent); err != nil {
					return err
				}
				startCalls++
				if startCalls == 1 {
					r.active = tc.unit == "active"
					return &localexec.Error{Kind: localexec.UnknownOutcome, ExitCode: -1}
				}
				if !inspected {
					t.Fatal("rollback start before inspection")
				}
				r.active = true
				return nil
			}
			system.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				if !hasUnknownEvent(r, "start_unit") {
					t.Fatal("inspection before unknown journal outcome")
				}
				inspected = true
				return systemd.Properties{ActiveState: tc.unit, SubState: tc.container}, nil
			}
			r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
				if startCalls == 1 {
					return podman.ContainerState{Running: tc.container == "running", Status: tc.container}, nil
				}
				return podman.ContainerState{Running: r.active, Status: map[bool]string{true: "running", false: "exited"}[r.active]}, nil
			}
			r.executor.Health = healthFunc(func(context.Context, policy.Desired, target.Port, bool) error {
				if startCalls == 1 && !tc.healthy {
					return injected
				}
				return nil
			})
			err := r.run()
			if tc.want == Succeeded {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				failure(t, err, tc.want, "start_unit")
			}
			if !inspected {
				t.Fatal("unit was not inspected")
			}
			if tc.want == RecoveryRequired && r.effects[len(r.effects)-1] != "start_unit" {
				t.Fatalf("mutated after unknown state: %v", r.effects)
			}
		})
	}
}

func hasUnknownEvent(r *rig, step string) bool {
	for _, event := range r.events {
		if event.Kind == "step" {
			var p ops.StepPayload
			if json.Unmarshal(event.Payload, &p) == nil && p.Step == step && p.Outcome == "unknown" {
				return true
			}
		}
	}
	return false
}

func TestUnknownMutationNeverRollsBackWithoutProof(t *testing.T) {
	for _, step := range []string{"pull_image", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "publish_route", "commit", "rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_route", "rollback_start"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			r.unknownStep = step
			if strings.HasPrefix(step, "rollback_") {
				r.failStep = "check_routed"
			}
			if step == "commit" {
				r.failCommitRead = true
			}
			unitInspections, containerInspections := 0, 0
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				if !hasUnknownEvent(r, step) {
					t.Fatal("unit inspection before unknown outcome")
				}
				unitInspections++
				return systemd.Properties{}, &localexec.Error{Kind: localexec.UnknownOutcome}
			}
			baseContainer := r.executor.Podman.(*podman.Fake).ContainerStateFunc
			r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(ctx context.Context, name podman.Name) (podman.ContainerState, error) {
				if hasUnknownEvent(r, step) {
					containerInspections++
					return podman.ContainerState{}, &localexec.Error{Kind: localexec.UnknownOutcome}
				}
				return baseContainer(ctx, name)
			}
			failure(t, r.run(), RecoveryRequired, step)
			if r.effects[len(r.effects)-1] != step {
				t.Fatalf("effect after unknown: %v", r.effects)
			}
			if unitInspections != 1 || containerInspections != 1 {
				t.Fatalf("unit/container inspections %d/%d", unitInspections, containerInspections)
			}
			if !hasUnknownEvent(r, step) {
				t.Fatal("unknown outcome missing")
			}
		})
	}
}

func TestPreflightUsesMergedDecisionFactsProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		disk target.Observation[uint64]
		want State
	}{
		{"enough capacity changed", target.Known(uint64(9 << 30)), Succeeded},
		{"below admission threshold", target.Known(uint64(1)), Failed},
		{"capacity unknown", target.Observation[uint64]{Status: target.Unknown}, Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, true)
			r.facts.Input.Snapshot.FreeDiskBytes = tc.disk
			err := r.run()
			if tc.want == Succeeded {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				failure(t, err, Failed, "preflight")
				if len(r.effects) != 1 {
					t.Fatalf("mutation after capacity refusal: %v", r.effects)
				}
			}
		})
	}
}

func stoppedOrRunningProbes(r *rig) {
	r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		if r.active {
			return systemd.Properties{ActiveState: "active", SubState: "running"}, nil
		}
		return systemd.Properties{ActiveState: "inactive", SubState: "dead"}, nil
	}
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		if r.active {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{Status: "exited"}, nil
	}
}

func TestUnknownPullUsesImmutableImageReadBack(t *testing.T) {
	for _, mode := range []string{"verified", "absent", "mismatch", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t, true)
			r.unknownStep = "pull_image"
			base := r.executor.Podman.(*podman.Fake).InspectFunc
			r.executor.Podman.(*podman.Fake).InspectFunc = func(ctx context.Context, image podman.Image) (podman.ImageInfo, error) {
				if r.intent != "pull_image" {
					return base(ctx, image)
				}
				if !hasUnknownEvent(r, "pull_image") {
					t.Fatal("image inspection before unknown journal outcome")
				}
				switch mode {
				case "verified":
					return podman.ImageInfo{IndexDigest: r.plan.Image.Digest, ManifestDigest: *r.plan.Image.ManifestDigest.Value, Platform: podman.Platform{OS: r.plan.Image.Platform.OS, Architecture: r.plan.Image.Platform.Arch}}, nil
				case "absent":
					return podman.ImageInfo{}, &localexec.Error{Kind: localexec.NotFound}
				case "mismatch":
					return podman.ImageInfo{IndexDigest: "different"}, nil
				default:
					return podman.ImageInfo{}, &localexec.Error{Kind: localexec.UnknownOutcome}
				}
			}
			err := r.run()
			switch mode {
			case "verified":
				if err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				failure(t, err, RecoveryRequired, "pull_image")
			default:
				failure(t, err, Failed, "pull_image")
			}
			if mode != "verified" && r.effects[len(r.effects)-1] != "pull_image" {
				t.Fatalf("mutation after unverified image: %v", r.effects)
			}
		})
	}
}

func TestUnknownStopUsesBothWriterProbes(t *testing.T) {
	for _, step := range []string{"quiesce_old", "rollback_quiesce"} {
		for _, stopped := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%v", step, stopped), func(t *testing.T) {
				r := newRig(t, true)
				r.unknownStep = step
				if step == "rollback_quiesce" {
					r.failStep = "check_direct"
				}
				stoppedOrRunningProbes(r)
				base := r.executor.Systemd.(*systemd.Fake).StopFunc
				r.executor.Systemd.(*systemd.Fake).StopFunc = func(ctx context.Context, unit systemd.Unit) error {
					err := base(ctx, unit)
					if r.intent == step && stopped {
						r.active = false
					}
					return err
				}
				err := r.run()
				if step == "quiesce_old" && stopped {
					if err != nil {
						t.Fatal(err)
					}
				} else if step == "quiesce_old" {
					failure(t, err, RolledBack, step)
				} else if stopped {
					failure(t, err, RolledBack, "check_direct")
				} else {
					failure(t, err, RecoveryRequired, step)
				}
			})
		}
	}
}

func TestUnknownArtifactUsesObservedHashNotWriterHealth(t *testing.T) {
	for _, step := range []string{"install_unit", "rollback_unit"} {
		for _, mode := range []string{"applied", "unchanged", "drift", "unknown inventory"} {
			t.Run(step+"/"+mode, func(t *testing.T) {
				r := newRig(t, true)
				r.unknownStep = step
				if step == "rollback_unit" {
					r.failStep = "check_direct"
				}
				stoppedOrRunningProbes(r)
				unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
				if err != nil {
					t.Fatal(err)
				}
				before, wanted := r.release.Units[0].Hash, unit.Hash()
				if step == "rollback_unit" {
					before, wanted = wanted, before
				}
				base := r.executor.Facts
				r.executor.Facts = FactsFunc(func(ctx context.Context) (Facts, error) {
					if r.intent == "preflight" {
						return base.Read(ctx)
					}
					if !hasUnknownEvent(r, step) {
						t.Fatal("artifact inspection before unknown journal outcome")
					}
					facts := r.facts
					apps := append([]target.App(nil), (*facts.Input.Snapshot.Apps.Value)...)
					units := append([]target.Unit(nil), (*apps[0].QuadletUnits.Value)...)
					hash := wanted
					switch mode {
					case "unchanged":
						hash = before
					case "drift":
						hash = "sha256:" + strings.Repeat("f", 64)
					case "unknown inventory":
						facts.Input.Snapshot.Apps = target.Observation[[]target.App]{Status: target.Unknown}
						return facts, nil
					}
					for i := range units {
						if units[i].Name == unit.Name() {
							units[i].Hash = hash
						}
					}
					apps[0].QuadletUnits = target.Known(units)
					facts.Input.Snapshot.Apps = target.Known(apps)
					return facts, nil
				})
				err = r.run()
				if mode == "applied" && step == "install_unit" {
					if err != nil {
						t.Fatal(err)
					}
				} else if mode == "applied" {
					failure(t, err, RolledBack, "check_direct")
				} else if mode == "unchanged" && step == "install_unit" {
					failure(t, err, RolledBack, step)
				} else {
					failure(t, err, RecoveryRequired, step)
				}
			})
		}
	}
}

func TestUnknownCommitUsesDurableReadBack(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			r := newRig(t, true)
			r.unknownStep = "commit"
			r.commitThenError = committed
			err := r.run()
			if committed {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				failure(t, err, RolledBack, "commit")
			}
			if !hasUnknownEvent(r, "commit") {
				t.Fatal("commit read-back hid unknown outcome")
			}
		})
	}
}

func TestUnknownJournalFailurePreventsInspection(t *testing.T) {
	r := newRig(t, true)
	r.unknownStep = "start_unit"
	r.failOutcome = "start_unit"
	r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		t.Fatal("inspection without durable unknown outcome")
		return systemd.Properties{}, nil
	}
	failure(t, r.run(), RecoveryRequired, "start_unit")
	if r.effects[len(r.effects)-1] != "start_unit" {
		t.Fatal(r.effects)
	}
}

func TestUnknownRollbackStartMustProvePreviousWriterHealth(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprint(running), func(t *testing.T) {
			r := newRig(t, true)
			r.unknownStep = "rollback_start"
			stoppedOrRunningProbes(r)
			base := r.executor.Systemd.(*systemd.Fake).StartFunc
			r.executor.Systemd.(*systemd.Fake).StartFunc = func(ctx context.Context, unit systemd.Unit) error {
				err := base(ctx, unit)
				if r.intent == "rollback_start" {
					r.active = running
				}
				return err
			}
			r.executor.Health = healthFunc(func(context.Context, policy.Desired, target.Port, bool) error {
				if r.intent == "check_direct" {
					return injected
				}
				return nil
			})
			err := r.run()
			if running {
				failure(t, err, RolledBack, "check_direct")
			} else {
				failure(t, err, RecoveryRequired, "rollback_start")
			}
		})
	}
}

func TestWriterInspectionRejectsPartialAndTransitionalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, active, sub, status string
		running                   bool
		unitErr, containerErr     error
		want                      writerState
	}{
		{"running", "active", "running", "running", true, nil, nil, writerRunning},
		{"stopped", "inactive", "dead", "exited", false, nil, nil, writerStopped},
		{"both absent", "", "", "", false, &localexec.Error{Kind: localexec.NotFound}, &localexec.Error{Kind: localexec.NotFound}, writerStopped},
		{"unit unreadable", "", "", "exited", false, injected, nil, writerUnknown},
		{"container unreadable", "inactive", "dead", "", false, nil, injected, writerUnknown},
		{"activating", "activating", "start", "running", true, nil, nil, writerUnknown},
		{"deactivating", "deactivating", "stop", "exited", false, nil, nil, writerUnknown},
		{"unknown container status", "inactive", "dead", "", false, nil, nil, writerUnknown},
		{"paused container", "inactive", "dead", "paused", false, nil, nil, writerUnknown},
		{"active oneshot", "active", "exited", "running", true, nil, nil, writerUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, true)
			r.executor.Systemd.(*systemd.Fake).ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
				return systemd.Properties{ActiveState: tc.active, SubState: tc.sub}, tc.unitErr
			}
			r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
				return podman.ContainerState{Status: tc.status, Running: tc.running}, tc.containerErr
			}
			x := execution{executor: &r.executor, plan: r.plan}
			if got := x.inspectWriter(context.Background()); got != tc.want {
				t.Fatalf("writer state %v, want %v", got, tc.want)
			}
		})
	}
}

type freshResolutionJournal struct {
	*rig
	t *testing.T
}

func (j freshResolutionJournal) AppendEvent(ctx context.Context, id string, event Event) (uint64, error) {
	var p ops.StepPayload
	if event.Kind == "step" && json.Unmarshal(event.Payload, &p) == nil && p.Step == "start_unit" && p.Outcome == "completed" {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 4750*time.Millisecond {
			j.t.Fatal("resolved outcome inherited the pre-inspection journal deadline")
		}
	}
	return j.rig.AppendEvent(ctx, id, event)
}
func TestResolvedOutcomeGetsFreshJournalBudget(t *testing.T) {
	r := newRig(t, true)
	r.unknownStep = "start_unit"
	stoppedOrRunningProbes(r)
	r.executor.Journal = freshResolutionJournal{r, t}
	r.executor.Health = healthFunc(func(ctx context.Context, _ policy.Desired, _ target.Port, _ bool) error {
		if r.intent == "start_unit" {
			timer := time.NewTimer(600 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
}
func TestCapacityAdmissionRemainsStateless(t *testing.T) {
	r := newRig(t, true)
	r.desired.MinimumFreeDiskBytes = 12345
	if !stateless(r.desired) {
		t.Fatal("capacity admission was misclassified as persistent data")
	}
}

func TestUnknownStartWithAbsentUnitAndContainerRollsBackFirstRelease(t *testing.T) {
	r := newRig(t, false)
	system := r.executor.Systemd.(*systemd.Fake)
	system.StartFunc = func(context.Context, systemd.Unit) error {
		r.hit("start_unit")
		return &localexec.Error{Kind: localexec.UnknownOutcome}
	}
	system.StopFunc = func(context.Context, systemd.Unit) error {
		r.hit(r.intent)
		return &localexec.Error{Kind: localexec.NotFound}
	}
	system.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{}, &localexec.Error{Kind: localexec.NotFound}
	}
	failure(t, r.run(), RolledBack, "start_unit")
	if r.candidate || r.active || r.committed {
		t.Fatal("failed first release was not removed safely")
	}
}

func TestUnknownReloadAndStageCannotBeProvenByWriterHealth(t *testing.T) {
	for _, step := range []string{"stage_unit", "reload_units", "publish_route", "rollback_reload", "rollback_route"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			r.unknownStep = step
			if strings.HasPrefix(step, "rollback_") {
				r.failStep = "check_routed"
			}
			stoppedOrRunningProbes(r)
			failure(t, r.run(), RecoveryRequired, step)
			if r.effects[len(r.effects)-1] != step {
				t.Fatalf("writer state incorrectly proved %s: %v", step, r.effects)
			}
		})
	}
}

func TestAbsentUnitCannotHideAWriterAppearingDuringRollback(t *testing.T) {
	r := newRig(t, false)
	system := r.executor.Systemd.(*systemd.Fake)
	system.StartFunc = func(context.Context, systemd.Unit) error {
		r.hit("start_unit")
		return &localexec.Error{Kind: localexec.UnknownOutcome}
	}
	system.StopFunc = func(context.Context, systemd.Unit) error {
		r.hit(r.intent)
		return &localexec.Error{Kind: localexec.NotFound}
	}
	system.ShowFunc = func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}
	inspections := 0
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		inspections++
		if inspections == 1 {
			return podman.ContainerState{}, &localexec.Error{Kind: localexec.NotFound}
		}
		return podman.ContainerState{Running: true, Status: "running"}, nil
	}
	failure(t, r.run(), RecoveryRequired, "rollback_quiesce")
	if !r.candidate || r.effects[len(r.effects)-1] != "rollback_quiesce" {
		t.Fatal("artifact restored while a writer remained")
	}
}
