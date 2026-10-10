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
	"github.com/ShaulLavo/brine/internal/localexec"
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
	operationKind   ops.Kind
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
	unknownStep     string
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
		secret := plan.SecretBinding{Environment: "TOKEN", Reference: "token", VersionName: "brine.hello.token.v2", ID: "fixture-secret-002"}
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
	r.executor = Executor{Journal: r, Releases: r, Plans: r, Facts: FactsFunc(r.readFacts), Units: r, Routes: r, Health: r, Compatibility: r, EffectTimeout: time.Second}
	r.executor.Podman = &podman.Fake{
		ContainerStateFunc: func(context.Context, podman.Name) (podman.ContainerState, error) {
			return podman.ContainerState{Running: r.active, Status: "exited"}, nil
		},
		PullFunc: func(context.Context, podman.Image) error { return r.hit("pull_image") },
		InspectFunc: func(context.Context, podman.Image) (podman.ImageInfo, error) {
			var err error
			if r.intent == "pull_image" {
				if r.unknownStep == "pull_image" {
					return podman.ImageInfo{}, &localexec.Error{Kind: localexec.UnknownOutcome}
				}
			} else {
				err = r.hit("verify_image")
			}
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
	configureSettledRecoveryWriter(r)
	return r
}
func (r *rig) readFacts(ctx context.Context) (Facts, error) {
	if r.intent == "preflight" {
		return r.facts, r.hit("preflight")
	}
	var outcome ops.StepPayload
	if len(r.events) == 0 || r.events[len(r.events)-1].Kind != "step" || json.Unmarshal(r.events[len(r.events)-1].Payload, &outcome) != nil || outcome.Step != r.intent || outcome.Outcome != "unknown" {
		panic("facts inspection without its journaled unknown outcome: " + r.intent)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		panic("unbounded facts inspection: " + r.intent)
	}
	return r.facts, ctx.Err()
}

func TestRigRejectsUnjournaledFactsInspection(t *testing.T) {
	for _, tc := range []struct {
		name, step, outcome string
		bounded             bool
	}{
		{"no event", "", "", true},
		{"intent only", "install_unit", "intent", true},
		{"completed outcome", "install_unit", "completed", true},
		{"different step", "rollback_unit", "unknown", true},
		{"unbounded", "install_unit", "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, true)
			if tc.step != "" {
				payload, err := json.Marshal(ops.StepPayload{Step: tc.step, Outcome: tc.outcome})
				if err != nil {
					t.Fatal(err)
				}
				r.events = []Event{{Kind: "step", Payload: payload}}
			}
			r.intent = "install_unit"
			ctx := context.Background()
			if tc.bounded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Second)
				defer cancel()
			}
			defer func() {
				if recover() == nil {
					t.Fatal("invalid inspection accepted")
				}
				if len(r.effects) != 0 {
					t.Fatal("inspection recorded a mutation")
				}
			}()
			_, _ = r.executor.Facts.Read(ctx)
		})
	}
}

func TestRigRejectsEffectWithoutMatchingIntent(t *testing.T) {
	r := newRig(t, true)
	r.intent = "preflight"
	defer func() {
		if got := recover(); got != "effect without its journaled intent: install_unit after preflight" {
			t.Fatalf("mutation guard panic = %v", got)
		}
		if len(r.effects) != 0 {
			t.Fatal("unjournaled mutation recorded")
		}
	}()
	_ = r.hit("install_unit")
}

func (r *rig) hit(step string) error {
	if r.intent != step {
		panic("effect without its journaled intent: " + step + " after " + r.intent)
	}
	r.effects = append(r.effects, step)
	if step == r.unknownStep {
		return &localexec.Error{Kind: localexec.UnknownOutcome, ExitCode: -1}
	}
	if step == r.failStep {
		return injected
	}
	return nil
}
func (r *rig) AppendEvent(_ context.Context, _ string, e Event) (uint64, error) {
	if e.Kind == "state" {
		return 0, ops.ErrInvalidEvent
	}
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
func (r *rig) SetOperationState(_ context.Context, _ string, s State) error {
	if r.failState == s {
		return injected
	}
	if !ops.CanTransition(r.state, s) {
		return errors.New("illegal state transition " + string(r.state) + " to " + string(s))
	}
	event := Event{Kind: "state", State: s, Sequence: uint64(len(r.events) + 1)}
	if err := ops.ValidateEvent(event); err != nil {
		return err
	}
	r.state = s
	r.states = append(r.states, s)
	r.events = append(r.events, event)
	return nil
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
func (r *rig) Install(_ context.Context, unit quadlet.Unit, _ string) error {
	if r.active {
		panic("install while previous writer is active")
	}
	err := r.hit("install_unit")
	r.candidate = true
	r.setLiveUnits([]target.Unit{{Name: unit.Name(), Hash: unit.Hash()}})
	return err
}
func (r *rig) Rollback(context.Context, string, string, string) error {
	if r.active {
		panic("restore unit while candidate writer is active")
	}
	err := r.hit("rollback_unit")
	if err == nil || r.unknownStep == "rollback_unit" {
		r.candidate = false
		r.setLiveUnits(r.release.Units)
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
		{"quiesce_old", RolledBack, []string{"rollback_check", "rollback_check"}},
		{"install_unit", RolledBack, []string{"rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"reload_units", RolledBack, []string{"rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"start_unit", RolledBack, []string{"rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"check_direct", RolledBack, []string{"rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"publish_route", RolledBack, []string{"rollback_quiesce", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"check_routed", RolledBack, []string{"rollback_quiesce", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
		{"commit", RolledBack, []string{"rollback_quiesce", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check", "rollback_check"}},
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
	for _, step := range []string{"rollback_quiesce", "rollback_route", "rollback_unit", "rollback_reload", "rollback_start", "rollback_check"} {
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
func TestStatelessRollbackNeedsNoExternalEvidence(t *testing.T) {
	r := newRig(t, true)
	r.failStep = "check_direct"
	r.executor.Compatibility = nil
	failure(t, r.run(), RolledBack, "check_direct")
	found := false
	for _, e := range r.events {
		if e.Kind == "step" {
			var p ops.StepPayload
			json.Unmarshal(e.Payload, &p)
			if p.Step == "check_compatibility" && p.Outcome == "completed" {
				found = p.Code == "stateless_compatible"
			}
		}
	}
	if !found {
		t.Fatal("missing typed stateless compatibility evidence")
	}
}
func TestUnclassifiedDataRequiresCompatibilityEvidence(t *testing.T) {
	for _, safe := range []bool{false, true} {
		r := newRig(t, true)
		r.compatibility = safe
		r.intent = "check_compatibility"
		x := &execution{executor: &r.executor, previousDesired: r.oldDesired, desired: r.desired}
		x.desired.SchemaVersion = 2
		err := x.checkCompatibility(context.Background())
		if safe && (err != nil || x.compatibilityBasis != "compatibility_verified") {
			t.Fatalf("%v %s", err, x.compatibilityBasis)
		}
		if !safe && err == nil {
			t.Fatal("unknown data classified as safe")
		}
	}
	r := newRig(t, true)
	r.executor.Compatibility = nil
	x := &execution{executor: &r.executor, previousDesired: r.oldDesired, desired: r.desired}
	x.desired.SchemaVersion = 2
	if err := x.checkCompatibility(context.Background()); err == nil {
		t.Fatal("missing evidence accepted")
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
	r.executor.Systemd.(*systemd.Fake).StartFunc = func(context.Context, systemd.Unit) error {
		r.hit(r.intent)
		r.executor.Systemd.(*systemd.Fake).JobPendingFunc = nil
		return context.DeadlineExceeded
	}
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

func TestLingeringContainerRefusesSecondWriter(t *testing.T) {
	r := newRig(t, true)
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		return podman.ContainerState{Running: true, Status: "running"}, nil
	}
	failure(t, r.run(), RecoveryRequired, "quiesce_old")
	if r.effects[len(r.effects)-1] != "quiesce_old" {
		t.Fatal(r.effects)
	}
}
func TestMissingContainerProvesQuiescence(t *testing.T) {
	r := newRig(t, true)
	r.active = false
	r.executor.Podman.(*podman.Fake).ContainerStateFunc = func(context.Context, podman.Name) (podman.ContainerState, error) {
		if r.active {
			return podman.ContainerState{Running: true, Status: "running"}, nil
		}
		return podman.ContainerState{}, &localexec.Error{Kind: localexec.NotFound}
	}
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
}

func TestUnclassifiedRollbackStopsBeforeRestoringOldWriter(t *testing.T) {
	r := newRig(t, true)
	r.state = Checking
	r.candidate = true
	r.active = true
	r.executor.Compatibility = nil
	service, err := systemd.ParseUnit("hello.service")
	if err != nil {
		t.Fatal(err)
	}
	x := &execution{executor: &r.executor, id: "operation-1", plan: r.plan, desired: r.desired, previous: r.release, previousDesired: r.oldDesired, hasPrevious: true, state: Checking, started: true, installed: true, service: service}
	x.previousDesired.SchemaVersion = 2
	failure(t, x.fail(context.Background(), &Error{Step: "check_direct", Code: "health_failed", Cause: injected}), RecoveryRequired, "check_compatibility")
	if r.active || !r.candidate {
		t.Fatal("candidate must remain stopped with artifacts retained")
	}
	if !reflect.DeepEqual(r.effects, []string{"rollback_quiesce"}) {
		t.Fatal(r.effects)
	}
}
func TestUnknownImagePullIsRecordedForReconciliation(t *testing.T) {
	r := newRig(t, true)
	r.executor.Podman.(*podman.Fake).PullFunc = func(context.Context, podman.Image) error { r.hit("pull_image"); return context.DeadlineExceeded }
	r.executor.Podman.(*podman.Fake).InspectFunc = func(context.Context, podman.Image) (podman.ImageInfo, error) {
		return podman.ImageInfo{}, &localexec.Error{Kind: localexec.UnknownOutcome}
	}
	failure(t, r.run(), RecoveryRequired, "pull_image")
	if r.effects[len(r.effects)-1] != "pull_image" {
		t.Fatal(r.effects)
	}
}

func TestRollbackJournalGolden(t *testing.T) {
	r := newRig(t, true)
	r.failStep = "check_routed"
	failure(t, r.run(), RolledBack, "check_routed")
	raw, err := json.MarshalIndent(r.events, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := "testdata/rollback.events.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, golden) {
		t.Fatalf("rollback journal differs from golden:\n%s", raw)
	}
}

func TestPublicJournalCannotAppendStateEvents(t *testing.T) {
	r := newRig(t, true)
	if _, err := r.AppendEvent(context.Background(), "operation-1", Event{Kind: "state", State: Preflight}); !errors.Is(err, ops.ErrInvalidEvent) {
		t.Fatalf("public state append: %v", err)
	}
	if r.state != Queued || len(r.events) != 0 {
		t.Fatal("refused append changed journal")
	}
	if err := r.SetOperationState(context.Background(), "operation-1", Preflight); err != nil {
		t.Fatal(err)
	}
	if len(r.events) != 1 || r.events[0].Kind != "state" || r.events[0].State != Preflight {
		t.Fatal("state transition did not create its own event")
	}
}
func TestExecutorEventsCarryStateOnlyOnTransitions(t *testing.T) {
	for _, fail := range []string{"", "check_routed", "start_unit"} {
		r := newRig(t, true)
		r.failStep = fail
		err := r.run()
		if fail == "" && err != nil {
			t.Fatal(err)
		}
		for _, event := range r.events {
			if event.Kind != "state" && event.State != "" {
				t.Fatalf("state leaked into %s event", event.Kind)
			}
		}
	}
}

func TestForwardHealthGetsFullValidatedDeadline(t *testing.T) {
	for _, seconds := range []int{900, spec.MaxStartupDeadlineSeconds} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) { testForwardHealthDeadline(t, seconds) })
	}
}
func testForwardHealthDeadline(t *testing.T, seconds int) {
	r := newRig(t, true)
	r.state = Checking
	x := &execution{executor: &r.executor, id: "operation-1", desired: r.desired, state: Checking}
	x.desired.Health.StartupDeadlineSeconds = seconds
	if err := x.step(context.Background(), "check_direct", Checking, "health_failed", func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < time.Duration(seconds-1)*time.Second {
			t.Errorf("health deadline truncated: %v", time.Until(deadline))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRollbackHealthGetsFullValidatedDeadlines(t *testing.T) {
	for _, seconds := range []int{900, spec.MaxStartupDeadlineSeconds} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) { testRollbackHealthDeadline(t, seconds) })
	}
}
func testRollbackHealthDeadline(t *testing.T, seconds int) {
	r := newRig(t, true)
	r.state = Checking
	r.candidate = true
	unit, err := quadlet.Render(r.desired, r.plan, *r.plan.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	service, err := systemd.ParseUnit("hello.service")
	if err != nil {
		t.Fatal(err)
	}
	x := &execution{executor: &r.executor, id: "operation-1", plan: r.plan, desired: r.desired, previous: r.release, previousDesired: r.oldDesired, hasPrevious: true, state: Checking, started: true, installed: true, unit: unit, service: service}
	x.previousDesired.Health.StartupDeadlineSeconds = seconds
	probes := 0
	r.executor.Health = healthFunc(func(ctx context.Context, _ policy.Desired, _ target.Port, _ bool) error {
		probes++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < time.Duration(seconds-1)*time.Second {
			t.Errorf("rollback health deadline truncated: %v", time.Until(deadline))
		}
		return nil
	})
	failure(t, x.fail(context.Background(), &Error{Step: "check_direct", Code: "health_failed", Cause: injected}), RolledBack, "check_direct")
	if probes != 2 {
		t.Fatalf("health probes %d", probes)
	}
}

func (r *rig) GetOperation(_ context.Context, id string) (ops.Operation, error) {
	kind := r.operationKind
	if kind == "" {
		kind = ops.Deploy
	}
	return ops.Operation{ID: id, Kind: kind, PlanID: r.plan.Hash, State: r.state}, nil
}

func (r *rig) VerifyCurrent(ctx context.Context, name string, hashes ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	hash, known := observedUnitHash(r.facts, r.plan.App, name)
	if !known {
		return errors.New("unreadable live unit")
	}
	for _, allowed := range hashes {
		if hash == allowed {
			return nil
		}
	}
	return errors.New("foreign live unit")
}
func (r *rig) setLiveUnits(units []target.Unit) {
	if r.facts.Input.Snapshot.Apps.Value == nil || len(*r.facts.Input.Snapshot.Apps.Value) == 0 {
		r.facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: r.plan.App}})
	}
	(*r.facts.Input.Snapshot.Apps.Value)[0].QuadletUnits = target.Known(units)
}
func TestPreflightTimeoutIsNotDrift(t *testing.T) {
	for _, honorsContext := range []bool{false, true} {
		t.Run(map[bool]string{false: "late_facts", true: "read_error"}[honorsContext], func(t *testing.T) {
			r := newRig(t, false)
			r.executor.EffectTimeout = 10 * time.Millisecond
			r.executor.Facts = FactsFunc(func(ctx context.Context) (Facts, error) {
				<-ctx.Done()
				if honorsContext {
					return Facts{}, ctx.Err()
				}
				return r.facts, nil
			})
			err := r.run()
			var failure *Error
			if !errors.As(err, &failure) || failure.Step != "preflight" || failure.Code != "inventory_failed" || r.state != Failed || !errors.Is(err, context.DeadlineExceeded) || len(r.effects) != 0 {
				t.Fatalf("error %v state %s effects %v", err, r.state, r.effects)
			}
		})
	}
}
