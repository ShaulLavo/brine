package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

var injected = errors.New("injected failure with private app output")

type rig struct {
	executor        Executor
	plan            plan.Plan
	desired         policy.Desired
	facts           Facts
	oldPlan         plan.Plan
	oldDesired      policy.Desired
	release         Release
	hasRelease      bool
	events          []Event
	states          []State
	effects         []string
	intent          string
	state           State
	failStep        string
	failJournal     string
	failState       State
	failOutcome     string
	unknownRoute    bool
	unknownRestore  bool
	compatibility   bool
	active          bool
	candidate       bool
	committed       bool
	failCommitRead  bool
	commitThenError bool
	healthTimeout   bool
}

func newRig(t testing.TB, update bool) *rig {
	t.Helper()
	read := func(path string) []byte {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	name := "ready-arm64"
	if update {
		name = "one-app"
	}
	snapshot, err := target.Decode(read("../target/testdata/" + name + ".json"))
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
	image := plan.Image{Digest: strings.Split(string(desired.Image), "@")[1], Platform: target.Platform{OS: "linux", Arch: snapshot.Arch}, ManifestDigest: target.Known("sha256:" + strings.Repeat("c", 64))}
	state := plan.BrineState{Target: snapshot.Identity, Generation: *snapshot.Generation.Value, Releases: []plan.CurrentRelease{}}
	r := &rig{desired: desired, oldDesired: desired, compatibility: true, active: update, state: Queued}
	if update {
		a := (*snapshot.Apps.Value)[0]
		// Include a real immutable secret binding in the executor matrix.
		desired.Secrets = []policy.Secret{{Name: "TOKEN", Reference: "token"}}
		r.oldDesired = desired
		secret := plan.SecretBinding{Environment: "TOKEN", Reference: "token", VersionName: "brine-hello-token-v2", ID: "fixture-secret-002"}
		current := plan.CurrentRelease{App: "hello", ID: "previous-release", Desired: desired, Image: image, HostPort: *a.AllocatedHostPort.Value, Secrets: []plan.SecretBinding{secret}, Units: *a.QuadletUnits.Value, CaddyFile: snapshot.CaddyConfig.Value.Files[0]}
		state.Releases = append(state.Releases, current)
		oldInput := plan.Input{Desired: desired, Snapshot: snapshot, Image: image, State: state}
		r.oldPlan, err = plan.Build(oldInput)
		if err != nil || r.oldPlan.Kind == plan.Conflict {
			t.Fatalf("old plan %v %v", r.oldPlan.Conflicts, err)
		}
		r.release = Release{ID: current.ID, PlanID: r.oldPlan.Hash, Image: image, HostPort: current.HostPort, Secrets: current.Secrets, Units: current.Units, CaddyFile: current.CaddyFile, CaddyGeneration: snapshot.CaddyConfig.Value.Generation}
		r.hasRelease = true
		desired.Environment = []policy.Environment{{Name: "APP_ENV", Value: "private-literal-not-in-events"}}
	}
	in := plan.Input{Desired: desired, Snapshot: snapshot, Image: image, State: state}
	r.plan, err = plan.Build(in)
	if err != nil || r.plan.Kind == plan.Conflict {
		t.Fatalf("plan %v %v", r.plan.Conflicts, err)
	}
	r.desired = desired
	route := caddy.State{Files: map[string]string{}}
	if snapshot.CaddyConfig.Value != nil {
		route.Generation = snapshot.CaddyConfig.Value.Generation
		for _, f := range snapshot.CaddyConfig.Value.Files {
			route.Files[f.Name] = f.Hash
		}
	}
	r.facts = Facts{Input: in, Routing: route}
	r.executor = Executor{Journal: r, Releases: r, Plans: r, Facts: FactsFunc(func(context.Context) (Facts, error) { err := r.hit("preflight"); return r.facts, err }), Units: r, Routes: r, Health: r, Compatibility: r, EffectTimeout: time.Second}
	r.executor.Podman = &podman.Fake{
		PullFunc: func(context.Context, podman.Image) error { return r.hit("pull_image") },
		InspectFunc: func(context.Context, podman.Image) (podman.ImageInfo, error) {
			err := r.hit("verify_image")
			return podman.ImageInfo{IndexDigest: r.plan.Image.Digest, ManifestDigest: *r.plan.Image.ManifestDigest.Value, Platform: podman.Platform{OS: r.plan.Image.Platform.OS, Architecture: r.plan.Image.Platform.Arch}}, err
		},
		SecretExistsFunc: func(context.Context, podman.Name) (bool, error) {
			err := r.hit("ensure_secrets")
			return err == nil, err
		},
	}
	r.executor.Systemd = &systemd.Fake{
		StopFunc: func(context.Context, systemd.Unit) error {
			err := r.hit(r.intent)
			if err == nil {
				r.active = false
			}
			return err
		},
		IsActiveFunc:     func(context.Context, systemd.Unit) (bool, error) { return r.active, nil },
		DaemonReloadFunc: func(context.Context) error { return r.hit(r.intent) },
		StartFunc:        func(context.Context, systemd.Unit) error { err := r.hit(r.intent); r.active = true; return err },
	}
	return r
}
func (r *rig) hit(step string) error {
	if r.intent != step {
		panic("effect without its journaled intent: " + step + " after " + r.intent)
	}
	r.effects = append(r.effects, step)
	if step == r.failStep {
		return injected
	}
	return nil
}
func (r *rig) AppendEvent(_ context.Context, _ string, e Event) (uint64, error) {
	if err := ops.ValidateEvent(e); err != nil {
		return 0, err
	}
	if e.Kind == "step" {
		var p ops.StepPayload
		json.Unmarshal(e.Payload, &p)
		if p.Step == r.failJournal && p.Outcome == "intent" || p.Step == r.failOutcome && p.Outcome != "intent" {
			return 0, injected
		}
		if p.Outcome == "intent" {
			r.intent = p.Step
		}
	}
	e.Sequence = uint64(len(r.events) + 1)
	r.events = append(r.events, e)
	return e.Sequence, nil
}
func (r *rig) SetOperationState(ctx context.Context, id string, s State) error {
	if r.failState == s {
		return injected
	}
	if !ops.CanTransition(r.state, s) {
		return errors.New("illegal state transition " + string(r.state) + " to " + string(s))
	}
	r.state = s
	r.states = append(r.states, s)
	_, err := r.AppendEvent(ctx, id, Event{Kind: "state", State: s})
	return err
}
func (r *rig) CurrentRelease(context.Context, string) (Release, bool, error) {
	if r.intent == "commit" && r.failCommitRead {
		return Release{}, false, injected
	}
	return r.release, r.hasRelease, nil
}
func (r *rig) CommitRelease(_ context.Context, _ string, release Release) error {
	err := r.hit("commit")
	if err == nil || r.commitThenError {
		r.release = release
		r.hasRelease = true
		r.committed = true
	}
	return err
}
func (r *rig) LoadPlan(context.Context, string) (plan.Plan, policy.Desired, error) {
	return r.oldPlan, r.oldDesired, nil
}
func (r *rig) Stage(_ context.Context, _ quadlet.Unit) error { return r.hit("stage_unit") }
func (r *rig) Install(_ context.Context, _ quadlet.Unit, _ string) error {
	if r.active {
		panic("install while previous writer is active")
	}
	err := r.hit("install_unit")
	r.candidate = true
	return err
}
func (r *rig) Rollback(context.Context, string, string, string) error {
	if r.active {
		panic("restore unit while candidate writer is active")
	}
	err := r.hit("rollback_unit")
	if err == nil {
		r.candidate = false
	}
	return err
}
func (r *rig) Publish(_ context.Context, before caddy.State, _ plan.Plan, _ policy.Desired) (caddy.Result, error) {
	err := r.hit("publish_route")
	result := caddy.Result{Outcome: caddy.Applied, Next: caddy.State{Generation: before.Generation + 1, Files: map[string]string{"hello.caddy": "sha256:" + strings.Repeat("e", 64)}}}
	if err != nil {
		result.Outcome = caddy.Unchanged
	}
	if r.unknownRoute {
		result.Outcome = caddy.Unknown
		err = &caddy.UnknownOutcomeError{Stage: "reload", Cause: context.DeadlineExceeded}
	}
	return result, err
}
func (r *rig) Restore(context.Context, caddy.State, caddy.State) error {
	err := r.hit("rollback_route")
	if r.unknownRestore {
		return &caddy.UnknownOutcomeError{Stage: "restore-reload", Cause: context.DeadlineExceeded}
	}
	return err
}
func (r *rig) Check(ctx context.Context, _ policy.Desired, _ target.Port, _ bool) error {
	if r.healthTimeout && r.intent == "check_direct" {
		return context.DeadlineExceeded
	}
	return r.hit(r.intent)
}
func (r *rig) Safe(context.Context, policy.Desired, policy.Desired) (bool, error) {
	err := r.hit("check_compatibility")
	return r.compatibility, err
}
func (r *rig) run() error {
	return r.executor.Run(context.Background(), "operation-1", r.plan, r.desired)
}
func failure(t testing.TB, err error, state State, step string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.State != state || step != "" && e.Step != step {
		t.Fatalf("error %+v want state %s step %s", err, state, step)
	}
}

func TestSuccessAndDeterministicJournal(t *testing.T) {
	r := newRig(t, true)
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !r.committed || r.release.PlanID != r.plan.Hash {
		t.Fatalf("state %s release %+v", r.state, r.release)
	}
	want := []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "check_direct", "publish_route", "check_routed", "commit"}
	if !reflect.DeepEqual(r.effects, want) {
		t.Fatalf("effects %v", r.effects)
	}
	raw, err := json.MarshalIndent(r.events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := "testdata/success.events.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, golden) {
		t.Fatalf("journal differs from golden:\n%s", raw)
	}
	if bytes.Contains(raw, []byte("private-literal")) || bytes.Contains(raw, []byte("private app output")) {
		t.Fatal("journal leaked values")
	}
}

func TestFailureAtEveryForwardStep(t *testing.T) {
	tests := []struct {
		step     string
		state    State
		rollback []string
	}{
		{"preflight", Failed, nil}, {"pull_image", Failed, nil}, {"verify_image", Failed, nil}, {"ensure_secrets", Failed, nil}, {"stage_unit", Failed, nil},
		{"quiesce_old", RolledBack, []string{"rollback_start", "rollback_check", "rollback_check"}},
		{"install_unit", RolledBack, []string{"rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"reload_units", RolledBack, []string{"rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"start_unit", RolledBack, []string{"rollback_quiesce", "check_compatibility", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"check_direct", RolledBack, []string{"rollback_quiesce", "check_compatibility", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"publish_route", RolledBack, []string{"rollback_quiesce", "check_compatibility", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"check_routed", RolledBack, []string{"rollback_quiesce", "check_compatibility", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"commit", RolledBack, []string{"rollback_quiesce", "check_compatibility", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
	}
	for _, test := range tests {
		t.Run(test.step, func(t *testing.T) {
			r := newRig(t, true)
			r.failStep = test.step
			failure(t, r.run(), test.state, test.step)
			idx := 0
			for i, s := range r.effects {
				if s == test.step {
					idx = i + 1
					break
				}
			}
			if !reflect.DeepEqual(r.effects[idx:], test.rollback) && !(len(r.effects[idx:]) == 0 && len(test.rollback) == 0) {
				t.Fatalf("rollback %v want %v", r.effects[idx:], test.rollback)
			}
			if r.committed {
				t.Fatal("committed failed release")
			}
		})
	}
}

func TestRollbackFailureRequiresRecovery(t *testing.T) {
	for _, step := range []string{"rollback_quiesce", "check_compatibility", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			original := r.executor.Health
			r.executor.Health = healthFunc(func(ctx context.Context, d policy.Desired, p target.Port, routed bool) error {
				if r.intent == "check_routed" {
					r.effects = append(r.effects, "check_routed")
					return injected
				}
				return original.Check(ctx, d, p, routed)
			})
			r.failStep = step
			failure(t, r.run(), RecoveryRequired, step)
			if r.effects[len(r.effects)-1] != step {
				t.Fatalf("continued after failed rollback %v", r.effects)
			}
		})
	}
}

type healthFunc func(context.Context, policy.Desired, target.Port, bool) error

func (f healthFunc) Check(c context.Context, d policy.Desired, p target.Port, r bool) error {
	return f(c, d, p, r)
}

func TestJournalFailuresStopBeforeEffect(t *testing.T) {
	for _, step := range []string{"preflight", "pull_image", "verify_image", "ensure_secrets", "stage_unit", "quiesce_old", "install_unit", "reload_units", "start_unit", "check_direct", "publish_route", "check_routed", "commit"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			r.failJournal = step
			failure(t, r.run(), RecoveryRequired, step)
			for _, effect := range r.effects {
				if effect == step {
					t.Fatal("effect ran without durable intent")
				}
			}
		})
	}
}
func TestOutcomeJournalFailureStopsWithoutRollback(t *testing.T) {
	r := newRig(t, true)
	r.failOutcome = "start_unit"
	failure(t, r.run(), RecoveryRequired, "start_unit")
	if r.effects[len(r.effects)-1] != "start_unit" {
		t.Fatal(r.effects)
	}
}
func TestStateWriteFailureStopsBeforeEffect(t *testing.T) {
	r := newRig(t, true)
	r.failState = Starting
	failure(t, r.run(), RecoveryRequired, "install_unit")
	if r.effects[len(r.effects)-1] != "quiesce_old" {
		t.Fatal(r.effects)
	}
}
func TestHashMismatchRefusesBeforeEffects(t *testing.T) {
	r := newRig(t, true)
	r.desired.Environment[0].Value = "different"
	failure(t, r.run(), Failed, "preflight")
	if len(r.effects) != 0 {
		t.Fatal(r.effects)
	}
}
func TestDriftRefusesBeforeMutation(t *testing.T) {
	for _, change := range []func(*rig){
		func(r *rig) { r.facts.Input.Snapshot.Identity.ID = "different" },
		func(r *rig) { r.facts.Input.State.Generation++ },
		func(r *rig) { r.facts.Input.Desired.PolicyHash = "sha256:" + strings.Repeat("f", 64) },
		func(r *rig) { r.plan.HostPort++ },
		func(r *rig) { r.release.Units = nil },
	} {
		r := newRig(t, true)
		change(r)
		failure(t, r.run(), Failed, "preflight")
		if !reflect.DeepEqual(r.effects, []string{"preflight"}) {
			t.Fatal(r.effects)
		}
	}
}
func TestUnknownRouteStopsWithoutRollback(t *testing.T) {
	r := newRig(t, true)
	r.unknownRoute = true
	failure(t, r.run(), RecoveryRequired, "publish_route")
	if r.effects[len(r.effects)-1] != "publish_route" {
		t.Fatal(r.effects)
	}
	var last ops.StepPayload
	for _, e := range r.events {
		if e.Kind == "step" {
			json.Unmarshal(e.Payload, &last)
		}
	}
	if last.Outcome != "unknown" || last.Code != "reload_unknown" {
		t.Fatal(last)
	}
}
func TestUnknownRestoreStopsWithoutFurtherEffects(t *testing.T) {
	r := newRig(t, true)
	r.failStep = "check_routed"
	r.unknownRestore = true
	failure(t, r.run(), RecoveryRequired, "rollback_route")
	if r.effects[len(r.effects)-1] != "rollback_route" {
		t.Fatal(r.effects)
	}
}
func TestUnknownCompatibilityLeavesCandidateStopped(t *testing.T) {
	r := newRig(t, true)
	r.failStep = "check_direct"
	r.compatibility = false
	failure(t, r.run(), RecoveryRequired, "check_compatibility")
	if r.active || !r.candidate {
		t.Fatal("candidate not stopped and retained for recovery")
	}
}
func TestHealthTimeoutRollsBack(t *testing.T) {
	r := newRig(t, true)
	r.healthTimeout = true
	err := r.run()
	failure(t, err, RolledBack, "check_direct")
	var e *Error
	errors.As(err, &e)
	if e.Code != "health_timeout" {
		t.Fatal(e)
	}
}
func TestFirstReleaseSuccessAndRollback(t *testing.T) {
	for _, fail := range []string{"", "check_routed"} {
		r := newRig(t, false)
		r.failStep = fail
		err := r.run()
		if fail == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			failure(t, err, RolledBack, fail)
			if r.active || r.candidate || r.hasRelease {
				t.Fatal("first release not removed")
			}
		}
	}
}
func TestCommitReadback(t *testing.T) {
	t.Run("committed", func(t *testing.T) {
		r := newRig(t, true)
		r.failStep = "commit"
		r.commitThenError = true
		if err := r.run(); err != nil {
			t.Fatal(err)
		}
		if r.state != Succeeded {
			t.Fatal(r.state)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		r := newRig(t, true)
		r.failStep = "commit"
		r.failCommitRead = true
		failure(t, r.run(), RecoveryRequired, "commit")
		if r.effects[len(r.effects)-1] != "commit" {
			t.Fatal(r.effects)
		}
	})
}

func TestImageVerificationMatrix(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*podman.ImageInfo)
		code   string
	}{
		{"index", func(i *podman.ImageInfo) { i.IndexDigest = "sha256:" + strings.Repeat("f", 64) }, "digest_mismatch"},
		{"manifest", func(i *podman.ImageInfo) { i.ManifestDigest = "sha256:" + strings.Repeat("f", 64) }, "digest_mismatch"},
		{"arch", func(i *podman.ImageInfo) { i.Platform.Architecture = "amd64" }, "platform_mismatch"},
		{"os", func(i *podman.ImageInfo) { i.Platform.OS = "other" }, "platform_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newRig(t, true)
			f := r.executor.Podman.(*podman.Fake)
			original := f.InspectFunc
			f.InspectFunc = func(c context.Context, i podman.Image) (podman.ImageInfo, error) {
				got, err := original(c, i)
				test.mutate(&got)
				return got, err
			}
			err := r.run()
			failure(t, err, Failed, "verify_image")
			var e *Error
			errors.As(err, &e)
			if e.Code != test.code {
				t.Fatal(e)
			}
			if r.effects[len(r.effects)-1] != "verify_image" {
				t.Fatal(r.effects)
			}
		})
	}
}
func TestStopMustProveWriterOff(t *testing.T) {
	r := newRig(t, true)
	r.executor.Systemd.(*systemd.Fake).StopFunc = func(context.Context, systemd.Unit) error { return r.hit(r.intent) }
	failure(t, r.run(), RolledBack, "quiesce_old")
	for _, effect := range r.effects {
		if effect == "install_unit" {
			t.Fatal("installed while writer was active")
		}
	}
}
func TestUnknownStartDoesNotRetry(t *testing.T) {
	r := newRig(t, true)
	r.executor.Systemd.(*systemd.Fake).StartFunc = func(context.Context, systemd.Unit) error { r.hit(r.intent); return context.DeadlineExceeded }
	failure(t, r.run(), RecoveryRequired, "start_unit")
	if r.effects[len(r.effects)-1] != "start_unit" {
		t.Fatal(r.effects)
	}
}
func TestRollbackJournalFailuresStopBeforeEffect(t *testing.T) {
	for _, step := range []string{"rollback_quiesce", "check_compatibility", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check"} {
		t.Run(step, func(t *testing.T) {
			r := newRig(t, true)
			r.failStep = "check_routed"
			r.failJournal = step
			failure(t, r.run(), RecoveryRequired, step)
			for _, effect := range r.effects {
				if effect == step {
					t.Fatal("rollback effect ran without intent")
				}
			}
		})
	}
}
func TestNoOpDoesNotTouchHost(t *testing.T) {
	r := newRig(t, true)
	r.plan = r.oldPlan
	r.desired = r.oldDesired
	r.facts.Input.Desired = r.oldDesired
	if r.plan.Kind != plan.NoOp {
		t.Fatalf("fixture is %s", r.plan.Kind)
	}
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	if r.state != Succeeded || !reflect.DeepEqual(r.effects, []string{"preflight"}) || r.committed {
		t.Fatalf("%s %v", r.state, r.effects)
	}
}
