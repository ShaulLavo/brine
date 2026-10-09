// Package apply orchestrates verified deployment intent. Run requires the caller
// to hold the target mutation lock for its entire lifetime. It never authorizes
// an offline plan, acquires an agent credential, or rewinds application data.
//
// With context-honoring adapters, the maximum operation duration is
// 32*E + 3*Hcandidate + 3*Hprevious + 10 minutes, where E is EffectTimeout
// (one minute by default) and H is the validated startup deadline. At the
// spec maximum of 3600 seconds per H and default E, this is 6 hours 42 minutes.
// Caller cancellation can shorten execution. Rollback detaches cancellation
// but retains a budget for effects, health probes, read-only reconciliation
// of unknown outcomes and journal writes. Manager-job settlement uses E; health
// keeps its full H deadline. No indeterminate mutation is retried.
package apply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type Executor struct {
	Journal       Journal
	Releases      ReleaseStore
	Plans         PlanLoader
	Facts         FactsReader
	Podman        podman.Adapter
	Systemd       systemd.Adapter
	Units         Units
	Routes        Routes
	Health        Health
	Compatibility Compatibility
	// EffectTimeout bounds individual non-health steps. Zero uses one minute.
	EffectTimeout time.Duration
}

type execution struct {
	executor                                *Executor
	id                                      string
	plan                                    plan.Plan
	desired                                 policy.Desired
	facts                                   Facts
	previous                                Release
	previousDesired                         policy.Desired
	hasPrevious                             bool
	unit                                    quadlet.Unit
	service                                 systemd.Unit
	route                                   caddy.State
	quiesced, installed, started, published bool
	state                                   State
	compatibilityBasis                      string
	nextRelease                             *Release
}

func desiredMatches(p plan.Plan, d policy.Desired) bool {
	b, err := d.CanonicalBytes()
	return err == nil && p.DesiredHash == fmt.Sprintf("sha256:%x", sha256.Sum256(b)) && p.App == string(d.Name)
}

func (e *Executor) Run(ctx context.Context, opID string, p plan.Plan, d policy.Desired) error {
	return e.run(ctx, opID, p, d, nil)
}

func (e *Executor) run(ctx context.Context, opID string, p plan.Plan, d policy.Desired, recovery *Recovery) error {
	if e.Journal == nil {
		return &Error{Step: "preflight", Code: "journal_failed", State: Failed}
	}
	x := &execution{executor: e, id: opID, plan: p, desired: d}
	preflight := func(ctx context.Context) error {
		if d.Health.StartupDeadlineSeconds < 1 || d.Health.StartupDeadlineSeconds > spec.MaxStartupDeadlineSeconds {
			return errors.New("unvalidated health deadline")
		}
		if opID == "" || p.Image.ManifestDigest.Status != target.KnownStatus || p.Image.ManifestDigest.Value == nil || !desiredMatches(p, d) || (p.Kind != plan.Create && p.Kind != plan.Update && p.Kind != plan.NoOp) || len(p.Conflicts) != 0 {
			return errors.New("unverified input")
		}
		if e.Facts == nil || e.Releases == nil {
			return errors.New("preflight adapters required")
		}
		var err error
		x.facts, err = e.Facts.Read(ctx)
		if err != nil {
			return &Error{Step: "preflight", Code: "inventory_failed", Cause: err}
		}
		fresh, err := plan.Build(x.facts.Input)
		if err != nil {
			return err
		}
		original, err := p.CanonicalBytes()
		if err != nil {
			return err
		}
		rebuilt, err := fresh.CanonicalBytes()
		if err != nil || !bytes.Equal(original, rebuilt) {
			return errors.New("plan drift")
		}
		x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(ctx, p.App)
		if err != nil {
			return err
		}
		if (p.Kind == plan.Create && x.hasPrevious) || (p.Kind != plan.Create && !x.hasPrevious) {
			return errors.New("release drift")
		}
		if x.hasPrevious {
			if e.Plans == nil {
				return errors.New("previous plan required")
			}
			previousPlan, previousDesired, err := e.Plans.LoadPlan(ctx, x.previous.PlanID)
			if err != nil {
				return err
			}
			if previousDesired.Health.StartupDeadlineSeconds < 1 || previousDesired.Health.StartupDeadlineSeconds > spec.MaxStartupDeadlineSeconds || previousPlan.Hash != x.previous.PlanID || !desiredMatches(previousPlan, previousDesired) {
				return errors.New("previous input drift")
			}
			x.previousDesired = previousDesired
			found := false
			for _, r := range x.facts.Input.State.Releases {
				if r.App == p.App {
					found = r.ID == x.previous.ID && reflect.DeepEqual(r.Desired, previousDesired) && reflect.DeepEqual(r.Image, x.previous.Image) && r.HostPort == x.previous.HostPort && reflect.DeepEqual(r.Secrets, x.previous.Secrets) && reflect.DeepEqual(r.Units, x.previous.Units) && r.CaddyFile == x.previous.CaddyFile
				}
			}
			if !found {
				return errors.New("release store disagrees with facts")
			}
		}
		if p.Kind == plan.NoOp {
			return nil
		}
		if e.Podman == nil || e.Systemd == nil || e.Units == nil || e.Routes == nil || e.Health == nil {
			return errors.New("effect adapters required")
		}
		x.service, err = systemd.ParseUnit(p.App + ".service")
		if err != nil {
			return err
		}
		cfg := x.facts.Input.Snapshot.CaddyConfig
		if cfg.Status == target.Absent {
			if x.facts.Routing.Generation != 0 || len(x.facts.Routing.Files) != 0 {
				return errors.New("routing facts drift")
			}
		} else if cfg.Status != target.KnownStatus || cfg.Value == nil || cfg.Value.Generation != x.facts.Routing.Generation {
			return errors.New("routing facts required")
		}
		expected := map[string]string{}
		if cfg.Value != nil {
			for _, f := range cfg.Value.Files {
				expected[f.Name] = f.Hash
			}
		}
		if !reflect.DeepEqual(expected, x.facts.Routing.Files) {
			return errors.New("routing facts drift")
		}
		return nil
	}
	var err error
	if recovery == nil {
		err = x.step(ctx, "preflight", Preflight, "drift", preflight)
	} else {
		x.state = recovery.operation.State
		preflightCtx, preflightCancel := context.WithTimeout(ctx, e.effectTimeout())
		err = preflight(preflightCtx)
		if preflightCtx.Err() != nil {
			err = errors.Join(err, preflightCtx.Err())
		}
		preflightCancel()
		if err == nil && !recovery.completed["preflight"] {
			err = x.event(ctx, "preflight", "completed", "")
			if err == nil {
				recovery.completed["preflight"] = true
			}
		}
		x.quiesced, x.installed, x.started = recovery.execution.quiesced, recovery.execution.installed, recovery.execution.started
		x.unit = recovery.execution.unit
	}
	if err != nil {
		if recovery != nil {
			return recovery.execution.terminal(ctx, RecoveryRequired, &Error{Step: "preflight", Code: "interrupted", Cause: err})
		}
		return x.fail(ctx, err)
	}
	if p.Kind == plan.NoOp {
		return x.terminal(ctx, Succeeded, nil)
	}
	image, err := podman.ParseImage(string(d.Image))
	if err != nil {
		return x.fail(ctx, &Error{Step: "pull_image", Code: "digest_mismatch", Cause: err})
	}
	steps := []struct {
		name   string
		state  State
		code   string
		effect func(context.Context) error
	}{
		{"pull_image", Preparing, "digest_mismatch", func(ctx context.Context) error { return e.Podman.Pull(ctx, image) }},
		{"verify_image", Preparing, "digest_mismatch", func(ctx context.Context) error {
			info, err := e.Podman.Inspect(ctx, image)
			if err != nil {
				return err
			}
			if p.Image.ManifestDigest.Status != target.KnownStatus || p.Image.ManifestDigest.Value == nil || info.IndexDigest != p.Image.Digest || info.ManifestDigest != *p.Image.ManifestDigest.Value {
				return errors.New("image digest differs")
			}
			if info.Platform.OS != p.Image.Platform.OS || info.Platform.Architecture != p.Image.Platform.Arch {
				return &Error{Step: "verify_image", Code: "platform_mismatch"}
			}
			return nil
		}},
		{"ensure_secrets", Preparing, "secret_missing", func(ctx context.Context) error {
			for _, s := range p.Secrets {
				name, err := podman.ParseName(s.VersionName)
				if err != nil {
					return err
				}
				exists, err := e.Podman.SecretExists(ctx, name)
				if err != nil {
					return err
				}
				if !exists {
					return errors.New("bound secret version missing")
				}
			}
			return nil
		}},
		{"stage_unit", Preparing, "unit_invalid", func(ctx context.Context) error {
			var err error
			x.unit, err = quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
			if err != nil {
				return err
			}
			return e.Units.Stage(ctx, x.unit)
		}},
		{"quiesce_old", Quiescing, "stop_failed", func(ctx context.Context) error {
			if !x.hasPrevious {
				return nil
			}
			x.quiesced = true
			return x.stop(ctx)
		}},
		{"install_unit", Starting, "unit_failed", func(ctx context.Context) error {
			x.installed = true
			return e.Units.Install(ctx, x.unit, x.previousUnitHash())
		}},
		{"reload_units", Starting, "unit_failed", e.Systemd.DaemonReload},
		{"start_unit", Starting, "start_failed", func(ctx context.Context) error {
			x.started = true
			return e.Systemd.Start(ctx, x.service)
		}},
		{"check_direct", Checking, "health_failed", func(ctx context.Context) error { return x.check(ctx, d, p.HostPort, false) }},
		{"publish_route", Checking, "route_invalid", func(ctx context.Context) error {
			result, err := e.Routes.Publish(ctx, x.facts.Routing, p, d)
			x.route = result.Next
			x.published = result.Outcome == caddy.Applied
			if result.Outcome == caddy.Unknown {
				return &Error{Step: "publish_route", Code: "reload_unknown", Cause: err}
			}
			if result.Outcome == caddy.RecoveryRequired {
				return &Error{Step: "publish_route", Code: "rollback_failed", Cause: err}
			}
			if err != nil {
				return err
			}
			if result.Outcome != caddy.Applied {
				return errors.New("route not published")
			}
			return nil
		}},
		{"check_routed", Checking, "health_failed", func(ctx context.Context) error { return x.check(ctx, d, p.HostPort, true) }},
		{"commit", Committing, "commit_failed", func(ctx context.Context) error {
			units := []target.Unit{}
			for _, old := range x.previous.Units {
				if old.Name != x.unit.Name() {
					units = append(units, old)
				}
			}
			units = append(units, target.Unit{Name: x.unit.Name(), Hash: x.unit.Hash()})
			slices.SortFunc(units, func(a, b target.Unit) int { return strings.Compare(a.Name, b.Name) })
			release := Release{ID: opID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: units, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: x.route.Files[p.App+".caddy"]}, CaddyGeneration: x.route.Generation}
			x.nextRelease = &release
			err := e.Releases.CommitRelease(ctx, p.App, release)
			if err == nil {
				return nil
			}
			if isUnknown(err) {
				return err
			}
			// A commit error is not proof that the transaction did not commit.
			current, exists, readErr := e.Releases.CurrentRelease(ctx, p.App)
			if readErr != nil {
				return &Error{Step: "commit", Code: "interrupted", Cause: errors.Join(err, readErr)}
			}
			if exists && reflect.DeepEqual(current, release) {
				return nil
			}
			if exists != x.hasPrevious || (exists && !reflect.DeepEqual(current, x.previous)) {
				return &Error{Step: "commit", Code: "interrupted", Cause: err}
			}
			return err
		}},
	}
	for _, s := range steps {
		if recovery != nil && recovery.completed[s.name] {
			continue
		}
		if err := x.step(ctx, s.name, s.state, s.code, s.effect); err != nil {
			return x.fail(ctx, err)
		}
	}
	return x.terminal(ctx, Succeeded, nil)
}

func (x *execution) previousUnitHash() string {
	for _, u := range x.previous.Units {
		if u.Name == x.plan.App+".container" {
			return u.Hash
		}
	}
	return ""
}
func (x *execution) stop(ctx context.Context) error {
	if err := x.executor.Systemd.Stop(ctx, x.service); err != nil {
		// An absent unit is quiescent only when the independent container probe
		// also proves no writer remains. Keep compatibility checks for attempted starts.
		if isNotFound(err) && x.inspectWriter(ctx) == writerStopped {
			return nil
		}
		return err
	}
	active, err := x.executor.Systemd.IsActive(ctx, x.service)
	if err != nil {
		return err
	}
	if active {
		return errors.New("writer still active")
	}
	name, err := podman.ParseName("systemd-" + x.plan.App)
	if err != nil {
		return err
	}
	container, err := x.executor.Podman.ContainerState(ctx, name)
	var runtimeErr *localexec.Error
	if errors.As(err, &runtimeErr) && runtimeErr.Kind == localexec.NotFound {
		return nil
	}
	if err != nil || container.Running {
		return &Error{Step: x.service.String(), Code: "interrupted", Cause: err}
	}
	return nil
}
func (x *execution) check(ctx context.Context, d policy.Desired, port target.Port, routed bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.Health.StartupDeadlineSeconds)*time.Second)
	defer cancel()
	active, err := x.executor.Systemd.IsActive(ctx, x.service)
	if err == nil && !active {
		err = errors.New("service not active")
	}
	if err == nil {
		err = x.executor.Health.Check(ctx, d, port, routed)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (x *execution) step(ctx context.Context, name string, state State, code string, effect func(context.Context) error) error {
	if x.state != state {
		stateCtx, stateCancel := context.WithTimeout(ctx, journalTimeout)
		err := x.executor.Journal.SetOperationState(stateCtx, x.id, state)
		stateCancel()
		if err != nil {
			return &Error{Step: name, Code: "journal_failed", Cause: err}
		}
		x.state = state
	}
	if err := x.event(ctx, name, "intent", ""); err != nil {
		return err
	}
	timeout := x.executor.effectTimeout()
	if name == "check_direct" || name == "check_routed" {
		timeout = time.Duration(x.desired.Health.StartupDeadlineSeconds) * time.Second
	}
	if name == "rollback_check" {
		timeout = 2 * time.Duration(x.previousDesired.Health.StartupDeadlineSeconds) * time.Second
	}
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	err := effect(stepCtx)
	if stepCtx.Err() != nil {
		err = errors.Join(err, stepCtx.Err())
	}
	cancel()
	failureCode := code
	outcome := "completed"
	if err != nil {
		outcome = "failed"
		var classified *Error
		if errors.As(err, &classified) && classified.Code != "" {
			code = classified.Code
		}
		if errors.Is(err, context.DeadlineExceeded) && (name == "check_direct" || name == "check_routed" || name == "rollback_check") {
			code = "health_timeout"
		}
		if isUnknown(err) && name != "check_direct" && name != "check_routed" && name != "rollback_check" && name != "verify_image" && name != "ensure_secrets" && name != "preflight" && name != "check_compatibility" {
			code = "interrupted"
			outcome = "unknown"
		}
		var reloadUnknown *caddy.UnknownOutcomeError
		if errors.As(err, &reloadUnknown) {
			code = "reload_unknown"
			outcome = "unknown"
		}
		if classified != nil && (classified.Code == "reload_unknown" || classified.Code == "interrupted") {
			code = classified.Code
			outcome = "unknown"
		}
	}
	// Once an outcome write fails, no further effect is allowed in this process.
	recordCtx := context.WithoutCancel(ctx)
	eventCode := ""
	if err != nil {
		eventCode = code
	} else if name == "check_compatibility" {
		eventCode = x.compatibilityBasis
	}
	if journalErr := x.event(recordCtx, name, outcome, eventCode); journalErr != nil {
		return journalErr
	}
	if outcome == "unknown" {
		resolution := x.reconcileUnknown(ctx, name)
		switch resolution {
		case applied:
			if journalErr := x.event(recordCtx, name, "completed", ""); journalErr != nil {
				return journalErr
			}
			return nil
		case notApplied:
			if journalErr := x.event(recordCtx, name, "failed", failureCode); journalErr != nil {
				return journalErr
			}
			return &Error{Step: name, Code: failureCode, Cause: err}
		}
	}
	if err != nil {
		return &Error{Step: name, Code: code, Cause: err}
	}
	return nil
}
func isUnknown(err error) bool {
	var runtime *localexec.Error
	if errors.As(err, &runtime) && (runtime.Kind == localexec.UnknownOutcome || runtime.Kind == localexec.Timeout) {
		return true
	}
	var caddyUnknown *caddy.UnknownOutcomeError
	var timeout interface{ Timeout() bool }
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, quadlet.ErrPublicationUnknown) || errors.As(err, &caddyUnknown) || (errors.As(err, &timeout) && timeout.Timeout())
}
func (x *execution) event(ctx context.Context, step, outcome, code string) error {
	ctx, cancel := context.WithTimeout(ctx, journalTimeout)
	defer cancel()
	payload, _ := json.Marshal(stepPayload{Step: step, Outcome: outcome, Code: code})
	_, err := x.executor.Journal.AppendEvent(ctx, x.id, Event{Kind: "step", Payload: payload})
	if err != nil {
		return &Error{Step: step, Code: "journal_failed", Cause: err}
	}
	return nil
}
func (x *execution) terminal(ctx context.Context, state State, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalTimeout)
	defer cancel()
	if cause != nil {
		code := "executor_failed"
		if state == RecoveryRequired {
			code = "recovery_required"
		}
		payload, _ := json.Marshal(struct {
			Code string `json:"code"`
		}{code})
		if _, err := x.executor.Journal.AppendEvent(ctx, x.id, Event{Kind: "failure", Payload: payload}); err != nil {
			return &Error{Step: "terminal", Code: "journal_failed", State: RecoveryRequired, Cause: errors.Join(cause, err)}
		}
	}
	stateCtx, stateCancel := context.WithTimeout(ctx, journalTimeout)
	err := x.executor.Journal.SetOperationState(stateCtx, x.id, state)
	stateCancel()
	if err != nil {
		return &Error{Step: "terminal", Code: "journal_failed", State: RecoveryRequired, Cause: errors.Join(cause, err)}
	}
	x.state = state
	var failure *Error
	if errors.As(cause, &failure) {
		failure.State = state
	}
	return cause
}
func (x *execution) fail(ctx context.Context, cause error) error {
	var failure *Error
	if !errors.As(cause, &failure) {
		failure = &Error{Step: "preflight", Code: "drift", Cause: cause}
		cause = failure
	}
	if failure.Code == "journal_failed" || failure.Code == "reload_unknown" || failure.Code == "interrupted" || failure.Code == "rollback_failed" {
		return x.terminal(ctx, RecoveryRequired, cause)
	}
	if !x.quiesced && !x.installed {
		return x.terminal(ctx, Failed, cause)
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*x.executor.effectTimeout()+3*time.Duration(x.previousDesired.Health.StartupDeadlineSeconds)*time.Second+4*time.Minute)
	defer cancel()
	step := func(name, code string, effect func(context.Context) error) error {
		return x.step(rollbackCtx, name, RollingBack, code, effect)
	}
	if x.started {
		if err := step("rollback_quiesce", "stop_failed", x.stop); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
		if x.hasPrevious {
			if err := step("check_compatibility", "compatibility_unknown", x.checkCompatibility); err != nil {
				return x.terminal(ctx, RecoveryRequired, err)
			}
		}
	}
	if x.published {
		if err := step("rollback_route", "rollback_failed", func(ctx context.Context) error { return x.executor.Routes.Restore(ctx, x.route, x.facts.Routing) }); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	}
	if x.installed {
		if err := step("rollback_unit", "rollback_failed", func(ctx context.Context) error {
			return x.executor.Units.Rollback(ctx, x.unit.Name(), x.unit.Hash(), x.previousUnitHash())
		}); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
		if err := step("rollback_reload", "rollback_failed", x.executor.Systemd.DaemonReload); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	}
	if x.hasPrevious {
		if err := step("rollback_start", "rollback_failed", func(ctx context.Context) error { return x.executor.Systemd.Start(ctx, x.service) }); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
		if err := step("rollback_check", "health_failed", func(ctx context.Context) error {
			if err := x.check(ctx, x.previousDesired, x.previous.HostPort, false); err != nil {
				return err
			}
			return x.check(ctx, x.previousDesired, x.previous.HostPort, true)
		}); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	}
	return x.terminal(ctx, RolledBack, cause)
}

// FactsFunc is useful when the host combines inventory.Collector, the policy
// normalizer and its durable store without introducing another domain package.
type FactsFunc func(context.Context) (Facts, error)

func (f FactsFunc) Read(ctx context.Context) (Facts, error) { return f(ctx) }

const journalTimeout = 5 * time.Second

func (e *Executor) effectTimeout() time.Duration {
	if e.EffectTimeout <= 0 {
		return time.Minute
	}
	return e.EffectTimeout
}
