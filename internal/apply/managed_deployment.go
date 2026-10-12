package apply

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type managedExecution struct {
	x                      *execution
	hadRelease             bool
	previousFrontendPermit string
	previousPlan           plan.Plan
	frontendBound          bool
	intentWritten          bool
	routeRecorded          bool
	engineService          systemd.Unit
}

func (e *Executor) runManagedDeployment(ctx context.Context, id string, p plan.Plan, d policy.Desired) error {
	if p.Lifecycle != "" {
		return e.runManagedLifecycle(ctx, id, p, d)
	}
	run := &managedExecution{x: &execution{executor: e, id: id, plan: p, desired: d}}
	x := run.x

	if err := x.step(ctx, "preflight", Preflight, "drift", run.preflight); err != nil {
		return x.fail(ctx, err)
	}
	if p.Kind == plan.NoOp {
		return x.terminal(ctx, Succeeded, nil)
	}
	if err := run.setDesired(ctx, "managed_desired", Preparing, p.ManagedIntentRevision, plan.DesiredComponentsForDeployment(*p.Managed)); err != nil {
		return run.fail(ctx, err)
	}
	run.intentWritten = true
	if err := x.step(ctx, "engine_stage", Preparing, "unit_invalid", func(ctx context.Context) error {
		if !managedChange(p, plan.StartManagedEngine) {
			return e.Managed.VerifyEngineUnit(ctx, p)
		}
		return e.Managed.StageEngine(ctx, p, x.previous)
	}); err != nil {
		return run.fail(ctx, err)
	}
	if p.Managed.Frontend != nil {
		if err := run.prepareFrontend(ctx); err != nil {
			return run.fail(ctx, err)
		}
	}
	if x.hasPrevious {
		if err := x.step(ctx, "frontend_stop", Quiescing, "stop_failed", func(ctx context.Context) error { x.quiesced = true; return x.stop(ctx) }); err != nil {
			return run.fail(ctx, err)
		}
	}
	if managedChange(p, plan.StartManagedEngine) {
		engineSteps := []struct {
			name   string
			code   string
			effect func(context.Context) error
		}{
			{"engine_install", "unit_failed", func(ctx context.Context) error { return e.Managed.InstallEngine(ctx, p, x.previous) }},
			{"engine_reload", "unit_failed", e.Systemd.DaemonReload},
			{"engine_start", "start_failed", func(ctx context.Context) error {
				if err := e.Managed.BeforeEngineStart(ctx, p); err != nil {
					return err
				}
				return e.Systemd.Start(ctx, run.engineService)
			}},
		}
		for _, step := range engineSteps {
			if err := x.step(ctx, step.name, Starting, step.code, step.effect); err != nil {
				return run.fail(ctx, err)
			}
		}
	}
	if err := x.step(ctx, "engine_check", Starting, "health_failed", func(ctx context.Context) error { return e.Managed.EngineReady(ctx, p) }); err != nil {
		return run.fail(ctx, err)
	}
	if p.Managed.Frontend != nil {
		if err := run.startFrontend(ctx); err != nil {
			return run.fail(ctx, err)
		}
	}
	if managedChange(p, plan.RemoveManagedFrontend) {
		return x.terminal(ctx, RecoveryRequired, errors.New("managed frontend removal not yet admitted"))
	}
	if err := x.step(ctx, "managed_route", Checking, "route_invalid", func(ctx context.Context) error {
		if err := e.Managed.BeforeIngress(ctx, id, p, d); err != nil {
			return err
		}
		result, err := e.Routes.Publish(ctx, x.facts.Routing, p, d)
		x.route = result.Next
		x.published = result.Outcome == caddy.Applied
		if result.Outcome == caddy.Unknown {
			return &Error{Step: "managed_route", Code: "reload_unknown", Cause: err}
		}
		if err != nil {
			return err
		}
		if result.Outcome != caddy.Applied {
			return errors.New("managed route not published")
		}
		return nil
	}); err != nil {
		return run.fail(ctx, err)
	}
	if err := run.recordRoutes(ctx, p.ManagedRouteRevision, x.facts.Routing, x.route); err != nil {
		return run.fail(ctx, err)
	}
	run.routeRecorded = true
	if err := x.step(ctx, "managed_route_check", Checking, "health_failed", func(ctx context.Context) error {
		if err := e.Managed.BeforeIngress(ctx, id, p, d); err != nil {
			return err
		}
		if p.Managed.Frontend == nil {
			return nil
		}
		return x.check(ctx, d, p.HostPort, true)
	}); err != nil {
		return run.fail(ctx, err)
	}
	if err := x.step(ctx, "managed_commit", Committing, "commit_failed", func(ctx context.Context) error {
		if err := e.Managed.BeforeIngress(ctx, id, p, d); err != nil {
			return err
		}
		m := p.Managed
		engine := target.Unit{Name: run.engineService.String(), Hash: "sha256:" + m.EngineUnitHash}
		units := []target.Unit{engine}
		adjunct := &ops.ManagedRelease{BindingID: m.Database.BindingID, EpochID: m.Archive.EpochID, RuntimeSHA256: m.RuntimeSHA256, EngineUnit: engine, EnginePort: m.EnginePort}
		if m.Frontend != nil {
			front := target.Unit{Name: x.unit.Name(), Hash: x.unit.Hash()}
			units = append(units, front)
			adjunct.Frontend = &ops.ManagedFrontend{Image: m.Frontend.Image, Port: m.Frontend.Port, Unit: front, Secrets: slices.Clone(m.Frontend.Secrets)}
		}
		release := Release{ID: id, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: slices.Clone(p.Secrets), Units: units, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: x.route.Files[p.App+".caddy"]}, CaddyGeneration: x.route.Generation, Managed: adjunct}
		x.nextRelease = &release
		err := e.Releases.CommitRelease(ctx, p.App, release)
		if err == nil {
			return nil
		}
		current, exists, readErr := e.Releases.CurrentRelease(ctx, p.App)
		if readErr == nil && exists && reflect.DeepEqual(current, release) {
			return nil
		}
		return &Error{Step: "managed_commit", Code: "interrupted", Cause: errors.Join(err, readErr)}
	}); err != nil {
		return run.fail(ctx, err)
	}
	return x.terminal(ctx, Succeeded, nil)
}

func managedChange(p plan.Plan, kind plan.ChangeKind) bool {
	for _, change := range p.Changes {
		if change.Kind == kind {
			return true
		}
	}
	return false
}

func (r *managedExecution) prepareFrontend(ctx context.Context) error {
	x := r.x
	e := x.executor
	p := x.plan
	d := x.desired
	image, err := podman.ParseImage(string(d.Image))
	if err != nil {
		return err
	}
	steps := []struct {
		name   string
		state  State
		code   string
		effect func(context.Context) error
	}{
		{"frontend_pull", Preparing, "digest_mismatch", func(ctx context.Context) error { return e.Podman.Pull(ctx, image) }},
		{"frontend_verify", Preparing, "digest_mismatch", func(ctx context.Context) error {
			actual, err := e.Podman.Inspect(ctx, image)
			if err != nil {
				return err
			}
			if p.Image.ManifestDigest.Value == nil || actual.IndexDigest != p.Image.Digest || actual.ManifestDigest != *p.Image.ManifestDigest.Value || actual.Platform.OS != p.Image.Platform.OS || actual.Platform.Architecture != p.Image.Platform.Arch {
				return errors.New("frontend image differs")
			}
			return nil
		}},
		{"frontend_secrets", Preparing, "secret_missing", func(ctx context.Context) error { return verifyManagedFrontendSecrets(ctx, e, p) }},
		{"frontend_stage", Preparing, "unit_invalid", func(ctx context.Context) error {
			var err error
			x.unit, err = quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
			if err != nil {
				return err
			}
			return e.Units.Stage(ctx, x.unit)
		}},
	}
	for _, step := range steps {
		if err := x.step(ctx, step.name, step.state, step.code, step.effect); err != nil {
			return err
		}
	}
	return nil
}

func (r *managedExecution) startFrontend(ctx context.Context) error {
	x := r.x
	e := x.executor
	p := x.plan
	d := x.desired
	steps := []struct {
		name   string
		state  State
		code   string
		effect func(context.Context) error
	}{
		{"frontend_install", Starting, "unit_failed", func(ctx context.Context) error {
			x.installed = true
			return e.Units.Install(ctx, x.unit, x.previousUnitHash())
		}},
		{"frontend_reload", Starting, "unit_failed", e.Systemd.DaemonReload},
		{"frontend_bind", Starting, "unit_failed", func(ctx context.Context) error {
			err := e.Managed.BindFrontend(ctx, x.id, p, x.unit.Hash())
			if err == nil {
				r.frontendBound = true
			}
			return err
		}},
		{"frontend_start", Starting, "start_failed", func(ctx context.Context) error {
			if err := e.Managed.BeforeFrontendStart(ctx, x.id, p, d, x.unit.Hash()); err != nil {
				return err
			}
			x.started = true
			return e.Systemd.Start(ctx, x.service)
		}},
		{"frontend_check", Checking, "health_failed", func(ctx context.Context) error { return x.check(ctx, d, p.HostPort, false) }},
	}
	for _, step := range steps {
		if err := x.step(ctx, step.name, step.state, step.code, step.effect); err != nil {
			return err
		}
	}
	return nil
}
func verifyManagedFrontendSecrets(ctx context.Context, e *Executor, p plan.Plan) error {
	for _, secret := range p.Secrets {
		name, err := podman.ParseName(secret.VersionName)
		if err != nil {
			return err
		}
		present, err := e.Podman.SecretExists(ctx, name)
		if err != nil {
			return err
		}
		if !present {
			return errors.New("frontend secret version missing")
		}
	}
	return nil
}

func (r *managedExecution) fail(ctx context.Context, cause error) error {
	return r.compensate(ctx, cause)
}
