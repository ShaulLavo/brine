//go:build linux

package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func fixture(t testing.TB) (*store.Store, ops.Operation, string) {
	t.Helper()
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	snapshot, err := target.Decode(read("../target/testdata/ready-arm64.json"))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(bytes.ReplaceAll(read("../policy/testdata/operator.toml"), []byte("Registry.Example.com:5000"), []byte("ghcr.io")))
	if err != nil {
		t.Fatal(err)
	}
	app, err := spec.Parse(bytes.ReplaceAll(read("../spec/testdata/valid-minimal.toml"), []byte("example/hello"), []byte("team/hello")))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := policy.Normalize(app, pol)
	if err != nil {
		t.Fatal(err)
	}
	input := plan.Input{Desired: desired, Snapshot: snapshot, Image: plan.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}, ManifestDigest: target.Known("sha256:" + strings.Repeat("b", 64))}, State: plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}}
	p, err := plan.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id, err := s.SavePlan(context.Background(), p, desired)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := s.CreateOperation(context.Background(), ops.Intent{Kind: ops.Deploy, PlanID: id}, "fixture-requester", "fixture-key")
	if err != nil {
		t.Fatal(err)
	}
	return s, op, dir
}

func absentRunner() *systemd.Fake {
	return &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
		return systemd.Properties{}, &localexec.Error{Kind: localexec.NotFound}
	}, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return false, nil }}
}
func launch(t testing.TB, s *store.Store, id, outcome string) {
	t.Helper()
	data, _ := json.Marshal(ops.LaunchPayload{Outcome: outcome})
	if _, err := s.AppendEvent(context.Background(), id, ops.Event{Kind: "launch", Payload: data}); err != nil {
		t.Fatal(err)
	}
}
func TestLaunchEvidenceMatrix(t *testing.T) {
	cases := []struct {
		name     string
		state    ops.State
		launch   string
		props    systemd.Properties
		probeErr error
		pending  bool
		want     ops.State
		action   string
	}{
		{name: "never_launched", state: ops.Queued, want: ops.Failed, action: "failed", probeErr: &localexec.Error{Kind: localexec.NotFound}},
		{name: "unknown_collected", state: ops.LaunchUnknown, launch: "unknown", want: ops.RecoveryRequired, action: "recovery_required", probeErr: &localexec.Error{Kind: localexec.NotFound}},
		{name: "intent_without_outcome", state: ops.Queued, launch: "intent", want: ops.RecoveryRequired, action: "recovery_required", probeErr: &localexec.Error{Kind: localexec.NotFound}},
		{name: "unknown_running", state: ops.LaunchUnknown, launch: "unknown", props: systemd.Properties{ActiveState: "active", SubState: "running"}, want: ops.LaunchUnknown, action: "running"},
		{name: "unknown_failed", state: ops.LaunchUnknown, launch: "unknown", props: systemd.Properties{ActiveState: "failed", SubState: "failed"}, want: ops.Failed, action: "failed"},
		{name: "unknown_finished", state: ops.LaunchUnknown, launch: "unknown", props: systemd.Properties{ActiveState: "inactive", SubState: "dead"}, want: ops.RecoveryRequired, action: "recovery_required"},
		{name: "unreadable_manager", state: ops.Queued, launch: "intent", probeErr: errors.New("unreadable"), want: ops.RecoveryRequired, action: "recovery_required"},
		{name: "pending_runner", state: ops.LaunchUnknown, launch: "unknown", props: systemd.Properties{ActiveState: "inactive", SubState: "dead"}, pending: true, want: ops.LaunchUnknown, action: "running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, op, _ := fixture(t)
			if tc.launch != "" {
				launch(t, s, op.ID, tc.launch)
			}
			if tc.state != ops.Queued {
				if err := s.SetOperationState(ctx, op.ID, tc.state); err != nil {
					t.Fatal(err)
				}
			}
			fake := &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) { return tc.props, tc.probeErr }, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) { return tc.pending, nil }}
			r := Reconciler{Store: s, Systemd: fake}
			before, _ := s.EventsAfter(ctx, op.ID, 0, 128)
			preview, err := r.DryRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := s.EventsAfter(ctx, op.ID, 0, 128)
			current, _ := s.GetOperation(ctx, op.ID)
			if len(before) != len(after) || current.State != tc.state {
				t.Fatal("dry run wrote durable state")
			}
			if len(preview.Outcomes) != 1 || preview.Outcomes[0].Action != tc.action || preview.Outcomes[0].After != tc.want {
				t.Fatalf("preview %+v", preview)
			}
			report, err := r.Reconcile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			current, _ = s.GetOperation(ctx, op.ID)
			if current.State != tc.want || report.Outcomes[0].After != tc.want {
				t.Fatalf("state %s report %+v", current.State, report)
			}
			again, err := r.Reconcile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want.IsTerminal() && len(again.Outcomes) != 0 {
				t.Fatalf("nonconvergent %+v", again)
			}
			final, _ := s.EventsAfter(ctx, op.ID, 0, 128)
			expected := len(after)
			if tc.want.IsTerminal() {
				expected += 2
			}
			if len(final) != expected {
				t.Fatalf("events %d want %d", len(final), expected)
			}
		})
	}
}

func TestRunnerDeadAndRecoveryRequiredPreserved(t *testing.T) {
	ctx := context.Background()
	s, op, _ := fixture(t)
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preparing); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(ops.StepPayload{Step: "stage_unit", Outcome: "intent"})
	if _, err := s.AppendEvent(ctx, op.ID, ops.Event{Kind: "step", Payload: data}); err != nil {
		t.Fatal(err)
	}
	r := Reconciler{Store: s, Systemd: absentRunner()}
	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Outcomes[0].After != ops.RecoveryRequired {
		t.Fatal(report)
	}
	before, _ := s.EventsAfter(ctx, op.ID, 0, 128)
	if _, err = r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := s.EventsAfter(ctx, op.ID, 0, 128)
	if len(before) != len(after) {
		t.Fatal("cleared recovery state")
	}
}
func TestRefusesHeldLockAndAllowsCallerOwnedLock(t *testing.T) {
	ctx := context.Background()
	s, op, _ := fixture(t)
	lock, err := s.AcquireHostLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	r := Reconciler{Store: s, Systemd: absentRunner(), LockTimeout: 20 * time.Millisecond}
	if _, err = r.Reconcile(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v", err)
	}
	current, _ := s.GetOperation(ctx, op.ID)
	if current.State != ops.Queued {
		t.Fatal("changed without lock")
	}
	report, err := r.ReconcileUnderLock(ctx, lock, op.ID, false)
	if err != nil || len(report.Outcomes) != 0 {
		t.Fatalf("report %+v error %v", report, err)
	}
}

func TestDryRunPositiveContinuationReadsPaginatedJournal(t *testing.T) {
	ctx := context.Background()
	s, op, _ := fixture(t)
	for i := 0; i < 130; i++ {
		launch(t, s, op.ID, "intent")
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preflight); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOperationState(ctx, op.ID, ops.Preparing); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"preflight", "pull_image"} {
		outcome := "completed"
		if step == "pull_image" {
			outcome = "intent"
		}
		data, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: outcome})
		if _, err := s.AppendEvent(ctx, op.ID, ops.Event{Kind: "step", Payload: data}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	r := Reconciler{Store: s, Systemd: absentRunner(), ExecutorFor: func(_ context.Context, current ops.Operation, p plan.Plan, d policy.Desired) (*apply.Executor, error) {
		calls++
		if current.ID != op.ID {
			t.Fatal(current.ID)
		}
		raw, err := os.ReadFile("../target/testdata/ready-arm64.json")
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := target.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		input := plan.Input{Desired: d, Snapshot: snapshot, Image: p.Image, State: plan.BrineState{Target: p.Target, Generation: *p.ObservedGeneration.Value, Releases: []plan.CurrentRelease{}}}
		executor := &apply.Executor{Journal: s, Releases: releaseAdapter{s}, Plans: s, Facts: apply.FactsFunc(func(context.Context) (apply.Facts, error) {
			return apply.Facts{Input: input, Routing: caddy.State{Files: map[string]string{}}}, nil
		}), Systemd: absentRunner(), Podman: &podman.Fake{ContainerStateFunc: func(context.Context, podman.Name) (podman.ContainerState, error) {
			return podman.ContainerState{}, &localexec.Error{Kind: localexec.NotFound}
		}, InspectFunc: func(context.Context, podman.Image) (podman.ImageInfo, error) {
			return podman.ImageInfo{IndexDigest: p.Image.Digest, ManifestDigest: *p.Image.ManifestDigest.Value, Platform: podman.Platform{OS: p.Image.Platform.OS, Architecture: p.Image.Platform.Arch}}, nil
		}}}
		return executor, nil
	}}
	report, err := r.DryRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].Action != "resume" || calls != 1 {
		t.Fatalf("report %+v factory calls %d", report, calls)
	}
	current, err := s.GetOperation(ctx, op.ID)
	if err != nil || current.State != ops.Preparing {
		t.Fatalf("state %s error %v", current.State, err)
	}
	events, err := s.EventsAfter(ctx, op.ID, 0, 256)
	if err != nil || len(events) != 134 {
		t.Fatalf("events %d error %v", len(events), err)
	}
}

type releaseAdapter struct{ *store.Store }

func (s releaseAdapter) CurrentRelease(ctx context.Context, app string) (ops.Release, bool, error) {
	release, err := s.Store.CurrentRelease(ctx, app)
	if errors.Is(err, store.ErrNotFound) {
		return ops.Release{}, false, nil
	}
	return release, err == nil, err
}

func TestSecretOperationsNeverEnterDeploymentRecovery(t *testing.T) {
	for _, state := range []ops.State{ops.Queued, ops.Preparing} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			s, deploy, _ := fixture(t)
			if err := s.SetOperationState(ctx, deploy.ID, ops.Failed); err != nil {
				t.Fatal(err)
			}
			secret, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.SecretSet, App: "hello", SecretRef: "token"}, "fixture-requester", "secret-key")
			if err != nil {
				t.Fatal(err)
			}
			if state == ops.Preparing {
				if err := s.SetOperationState(ctx, secret.ID, state); err != nil {
					t.Fatal(err)
				}
			}
			manager := &systemd.Fake{ShowFunc: func(context.Context, systemd.Unit) (systemd.Properties, error) {
				t.Fatal("secret queried detached deployment manager")
				return systemd.Properties{}, nil
			}, JobPendingFunc: func(context.Context, systemd.Unit) (bool, error) {
				t.Fatal("secret queried deployment jobs")
				return false, nil
			}}
			r := Reconciler{Store: s, Systemd: manager}
			preview, err := r.DryRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.GetOperation(ctx, secret.ID)
			if err != nil || current.State != state {
				t.Fatal("preview changed secret", current, err)
			}
			report, err := r.Reconcile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := state
			if state == ops.Preparing {
				want = ops.RecoveryRequired
			}
			current, err = s.GetOperation(ctx, secret.ID)
			if err != nil || current.State != want || len(report.Outcomes) != 1 || len(preview.Outcomes) != 1 || report.Outcomes[0].After != want {
				t.Fatal(current, report, preview, err)
			}
		})
	}
}
