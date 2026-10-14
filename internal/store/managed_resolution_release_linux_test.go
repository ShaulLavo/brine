//go:build linux

package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

func managedRouteCommitFixture(t *testing.T) (*Store, plan.Plan, Operation, Operation, Release) {
	t.Helper()
	ctx := context.Background()
	s, p, source := managedControlFrontendFixture(t, true)
	_, d, err := s.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := quadlet.Render(d, p, *p.Image.ManifestDigest.Value)
	if err != nil {
		t.Fatal(err)
	}
	wanted := ManagedDesiredState{BindingID: p.Managed.Database.BindingID, EpochID: p.Managed.Archive.EpochID, RuntimeSHA256: p.Managed.RuntimeSHA256, Revision: 1, PlanID: p.Hash, OperationID: source.ID, EngineRunning: true, FrontendRunning: true, IngressServing: true}
	if _, err = s.CommitManagedDesired(ctx, source.ID, 0, wanted); err != nil {
		t.Fatal(err)
	}
	if err = s.SetOperationState(ctx, source.ID, ops.Starting); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BindManagedFrontendPermit(ctx, source.ID, p.Hash, unit.Hash()); err != nil {
		t.Fatal(err)
	}
	engine := target.Unit{Name: "brine-engine-" + string(wanted.BindingID) + ".service", Hash: "sha256:" + p.Managed.EngineUnitHash}
	front := target.Unit{Name: unit.Name(), Hash: unit.Hash()}
	release := Release{ID: source.ID, PlanID: p.Hash, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: []target.Unit{engine, front}, CaddyFile: target.CaddyFile{Name: p.App + ".caddy", Hash: "sha256:" + strings.Repeat("a", 64)}, CaddyGeneration: 1, Managed: &ops.ManagedRelease{BindingID: wanted.BindingID, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256, EngineUnit: engine, EnginePort: p.Managed.EnginePort, Frontend: &ops.ManagedFrontend{Image: p.Managed.Frontend.Image, Port: p.Managed.Frontend.Port, Unit: front, Secrets: p.Secrets}}}
	route := ManagedRouteState{App: p.App, BindingID: wanted.BindingID, EpochID: wanted.EpochID, RuntimeSHA256: wanted.RuntimeSHA256, DesiredRevision: 1, PlanID: p.Hash, OperationID: source.ID, Revision: 1, Generation: 1, Present: true, FileHash: release.CaddyFile.Hash, RootSHA256: strings.Repeat("b", 64), LoadedSHA256: strings.Repeat("c", 64)}
	if _, err = s.CommitManagedRouteState(ctx, 0, route); err != nil {
		t.Fatal(err)
	}
	if err = s.SetOperationState(ctx, source.ID, ops.Checking); err != nil {
		t.Fatal(err)
	}
	steps := []string{"preflight", "managed_desired", "engine_stage", "frontend_pull", "frontend_verify", "frontend_secrets", "frontend_stage", "engine_check", "frontend_install", "frontend_reload", "frontend_bind", "frontend_start", "frontend_check", "managed_route", "managed_route_receipt"}
	for _, step := range steps {
		appendManagedStep(t, s, source.ID, step, "intent")
		appendManagedStep(t, s, source.ID, step, "completed")
	}
	appendManagedStep(t, s, source.ID, "managed_route_check", "intent")
	appendManagedStep(t, s, source.ID, "managed_route_check", "unknown")
	if err = s.SetOperationState(ctx, source.ID, ops.RecoveryRequired); err != nil {
		t.Fatal(err)
	}
	source.State = ops.RecoveryRequired
	resolver, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: source.ID}, "requester", "route-check-resolve")
	if err != nil {
		t.Fatal(err)
	}
	return s, p, source, resolver, release
}
func appendManagedStep(t *testing.T, s *Store, id, step, outcome string) {
	t.Helper()
	raw, _ := json.Marshal(ops.StepPayload{Step: step, Outcome: outcome})
	if _, err := s.AppendEvent(context.Background(), id, ops.Event{Kind: "step", Payload: raw}); err != nil {
		t.Fatal(err)
	}
}
func claimManagedRouteCommit(t *testing.T, s *Store, id string) {
	t.Helper()
	for _, state := range []ops.State{ops.Preflight, ops.Preparing, ops.Starting, ops.Checking} {
		if err := s.SetOperationState(context.Background(), id, state); err != nil {
			t.Fatal(err)
		}
	}
	appendManagedStep(t, s, id, "managed_route_check", "completed")
	if err := s.SetOperationState(context.Background(), id, ops.Committing); err != nil {
		t.Fatal(err)
	}
	appendManagedStep(t, s, id, "managed_commit", "intent")
}
func TestManagedResolutionReleaseCommitPreservesOriginalOwnershipAndRequiresTerminalProof(t *testing.T) {
	s, p, source, resolver, release := managedRouteCommitFixture(t)
	ctx := context.Background()
	original, err := s.EventsAfter(ctx, source.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitRelease(ctx, p.App, release); err == nil {
		t.Fatal("ordinary commit accepted terminal source")
	}
	generation, err := s.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitManagedResolutionRelease(ctx, resolver.ID, generation, release); err == nil {
		t.Fatal("unclaimed resolver committed")
	}
	claimManagedRouteCommit(t, s, resolver.ID)
	if err := s.CommitManagedResolutionRelease(ctx, resolver.ID, generation, release); err != nil {
		t.Fatal("proven late continuation refused", err, generation, p.ObservedGeneration)
	}
	committed, err := s.CurrentRelease(ctx, p.App)
	if err != nil || committed.ID != source.ID {
		t.Fatal("release owner changed", err)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
		t.Fatal("unfinished resolver granted boot")
	}
	appendManagedStep(t, s, resolver.ID, "managed_commit", "completed")
	if err := s.SetOperationState(ctx, resolver.ID, ops.Succeeded); err != nil {
		t.Fatal(err)
	}
	granted, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || granted.Pending || granted.Permit.OperationID != source.ID {
		t.Fatal("resolved joined proof not serving", err)
	}
	after, err := s.EventsAfter(ctx, source.ID, 0, 1024)
	if err != nil || len(original) != len(after) {
		t.Fatal("source journal changed", err)
	}
	op, err := s.GetOperation(ctx, source.ID)
	if err != nil || op.State != ops.RecoveryRequired {
		t.Fatal("source outcome changed", err)
	}
}
func TestManagedResolutionReleaseRefusesStaleAndForeignClaims(t *testing.T) {
	for _, name := range []string{"generation", "owner", "unit", "epoch", "route", "fence", "incomplete-check", "earlier-unknown"} {
		t.Run(name, func(t *testing.T) {
			s, p, source, resolver, release := managedRouteCommitFixture(t)
			ctx := context.Background()
			generation, err := s.Generation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if name != "incomplete-check" {
				claimManagedRouteCommit(t, s, resolver.ID)
			}
			switch name {
			case "generation":
				generation++
			case "owner":
				release.ID = resolver.ID
			case "unit":
				release.Managed.Frontend.Unit.Hash = "sha256:" + strings.Repeat("f", 64)
				release.Units[1] = release.Managed.Frontend.Unit
			case "epoch":
				release.Managed.EpochID = "foreign"
			case "route":
				release.CaddyFile.Hash = "sha256:" + strings.Repeat("f", 64)
			case "fence":
				if _, err := s.HoldDataFence(ctx, p.Managed.Database.DatabaseID, "foreign-data-task"); err != nil {
					t.Fatal(err)
				}
			case "earlier-unknown":
				if _, err := s.db.ExecContext(ctx, "DROP TRIGGER events_no_update"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, "UPDATE events SET payload=json_set(payload,'$.outcome','unknown') WHERE operation_id=? AND kind='step' AND json_extract(payload,'$.step')='frontend_start' AND json_extract(payload,'$.outcome')='completed'", source.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.CommitManagedResolutionRelease(ctx, resolver.ID, generation, release); err == nil {
				t.Fatal("stale or hostile recovery committed")
			}
			if _, err := s.CurrentRelease(ctx, p.App); err == nil {
				t.Fatal("refusal committed a release")
			}
		})
	}
}

type managedCommitReleases struct{ *Store }

func (s managedCommitReleases) CurrentRelease(ctx context.Context, app string) (apply.Release, bool, error) {
	release, err := s.Store.CurrentRelease(ctx, app)
	if errors.Is(err, ErrNotFound) {
		return release, false, nil
	}
	return release, err == nil, err
}

type managedCommitHost struct {
	apply.ManagedRuntime
	apply.Routes
	state   *Store
	owner   string
	route   caddy.State
	checks  []bool
	stopped bool
}

func (h *managedCommitHost) CompletedManaged(ctx context.Context, owner string, p plan.Plan, _ policy.Desired) error {
	if h.stopped {
		return ErrConflict
	}
	return h.controlProof(ctx, owner, p)
}
func (h *managedCommitHost) StoppedManagedFrontend(ctx context.Context, owner string, p plan.Plan, _ policy.Desired) error {
	if !h.stopped {
		return ErrConflict
	}
	return h.controlProof(ctx, owner, p)
}
func (h *managedCommitHost) controlProof(ctx context.Context, owner string, p plan.Plan) error {
	record, err := h.state.ReadManagedFrontendRecord(ctx, p.App)
	if err != nil || owner != h.owner || record.Resolution.Permit.OperationID != owner || record.Resolution.Permit.PlanID != p.Hash || record.Resolution.Permit.RuntimeSHA256 != p.Managed.RuntimeSHA256 {
		return ErrConflict
	}
	native, err := h.state.ReadEnginePermit(ctx, p.Managed.Database.BindingID)
	if err != nil || !native.AllowsVerification(time.Now().UTC()) {
		return ErrConflict
	}
	desired, err := h.state.ReadManagedDesired(ctx, p.Managed.Database.BindingID)
	if err != nil || desired.OperationID != owner || desired.PlanID != p.Hash || !desired.EngineRunning || !desired.FrontendRunning || !desired.IngressServing {
		return ErrConflict
	}
	route, err := h.state.ReadManagedRouteState(ctx, p.App)
	if err != nil || route.OperationID != owner || route.PlanID != p.Hash || route.DesiredRevision != desired.Revision || route.Generation != h.route.Generation || route.FileHash != h.route.Files[p.App+".caddy"] {
		return ErrConflict
	}
	return nil
}
func (h *managedCommitHost) InspectManagedLoaded(_ context.Context, route caddy.State, _ plan.Plan, _ policy.Desired) error {
	if !reflect.DeepEqual(route, h.route) {
		return ErrConflict
	}
	return nil
}
func (h *managedCommitHost) Check(_ context.Context, _ policy.Desired, _ target.Port, routed bool) error {
	h.checks = append(h.checks, routed)
	return nil
}
func TestManagedRoutePublicExecutorCommitsThroughActualStoreAndGrantsOnlyAfterResolverSuccess(t *testing.T) {
	s, p, source, resolver, expected := managedRouteCommitFixture(t)
	ctx := context.Background()
	_, d, err := s.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := s.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := &managedCommitHost{state: s, owner: source.ID, route: caddy.State{Generation: expected.CaddyGeneration, Files: map[string]string{expected.CaddyFile.Name: expected.CaddyFile.Hash}}}
	facts := apply.Facts{Routing: host.route, Input: plan.Input{Desired: d, State: plan.BrineState{Target: p.Target, Generation: generation}}}
	facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: p.App, Image: target.Known(target.Image{Digest: p.Image.Digest, Platform: p.Image.Platform}), AllocatedHostPort: target.Known(p.HostPort), QuadletUnits: target.Known([]target.Unit{expected.Managed.Frontend.Unit})}})
	executor := apply.Executor{Journal: s, Releases: managedCommitReleases{s}, Managed: host, Routes: host, Health: host, Facts: apply.FactsFunc(func(context.Context) (apply.Facts, error) { return facts, nil }), Systemd: &systemd.Fake{IsActiveFunc: func(context.Context, systemd.Unit) (bool, error) { return true, nil }}, Podman: &podman.Fake{SecretExistsFunc: func(context.Context, podman.Name) (bool, error) { return true, nil }}}
	events, err := s.EventsAfter(ctx, resolver.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := executor.InspectResolution(ctx, resolver, source, p, d, events)
	if err != nil || assessment.Action != apply.ResumeForward {
		t.Fatal(assessment.Action, err)
	}
	if err := executor.Recover(ctx, assessment); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(host.checks, []bool{false, true}) {
		t.Fatal("unexpected health probes", host.checks)
	}
	actual, err := s.CurrentRelease(ctx, p.App)
	if err != nil || actual.ID != source.ID || actual.Managed.Frontend.Unit != expected.Managed.Frontend.Unit {
		t.Fatal("wrong released ownership", err)
	}
	permit, err := s.ReadManagedFrontendPermit(ctx, p.App)
	if err != nil || permit.Permit.OperationID != source.ID || permit.Pending {
		t.Fatal("resolved client guard refused", err)
	}
	original, err := s.GetOperation(ctx, source.ID)
	if err != nil || original.State != ops.RecoveryRequired {
		t.Fatal("original receipt rewritten", err)
	}
	completed, err := s.GetOperation(ctx, resolver.ID)
	if err != nil || completed.State != ops.Succeeded {
		t.Fatal("resolver did not own completion", err)
	}
}

func TestManagedRouteRecoveryWakesOnlyTheOriginalOwnedFrontendOnce(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "known", true: "unknown-running"}[unknown], func(t *testing.T) {
			s, p, source, resolver, expected := managedRouteCommitFixture(t)
			ctx := context.Background()
			_, d, err := s.LoadPlan(ctx, p.Hash)
			if err != nil {
				t.Fatal(err)
			}
			generation, err := s.Generation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			host := &managedCommitHost{state: s, owner: source.ID, stopped: true, route: caddy.State{Generation: expected.CaddyGeneration, Files: map[string]string{expected.CaddyFile.Name: expected.CaddyFile.Hash}}}
			facts := apply.Facts{Routing: host.route, Input: plan.Input{Desired: d, State: plan.BrineState{Target: p.Target, Generation: generation}}}
			facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: p.App, Image: target.Known(target.Image{Digest: p.Image.Digest, Platform: p.Image.Platform}), AllocatedHostPort: target.Known(p.HostPort), QuadletUnits: target.Known([]target.Unit{expected.Managed.Frontend.Unit})}})
			starts := 0
			system := &systemd.Fake{IsActiveFunc: func(context.Context, systemd.Unit) (bool, error) { return !host.stopped, nil }, StartFunc: func(ctx context.Context, unit systemd.Unit) error {
				starts++
				if unit.String() != p.App+".service" {
					t.Fatal("recovery attempted engine start")
				}
				pending, err := s.ReadManagedFrontendPermit(ctx, p.App)
				if err != nil || !pending.Pending || pending.PendingOperationID != resolver.ID || pending.Permit.OperationID != source.ID {
					t.Fatal("ordinary start had no exact resolver permit", err, pending.PendingOperationID)
				}
				host.stopped = false
				if unknown {
					return &localexec.Error{Kind: localexec.UnknownOutcome}
				}
				return nil
			}}
			executor := apply.Executor{Journal: s, Releases: managedCommitReleases{s}, Managed: host, Routes: host, Health: host, Facts: apply.FactsFunc(func(context.Context) (apply.Facts, error) { return facts, nil }), Systemd: system, Podman: &podman.Fake{SecretExistsFunc: func(context.Context, podman.Name) (bool, error) { return true, nil }}}
			events, err := s.EventsAfter(ctx, resolver.ID, 0, 1024)
			if err != nil {
				t.Fatal(err)
			}
			assessment, err := executor.InspectResolution(ctx, resolver, source, p, d, events)
			if err != nil || assessment.Action != apply.ResumeForward {
				t.Fatal("owned stopped frontend recovery unavailable", assessment.Action, err)
			}
			err = executor.Recover(ctx, assessment)
			if unknown {
				if err == nil || starts != 1 {
					t.Fatal("unknown start not preserved", err, starts)
				}
				if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err == nil {
					t.Fatal("terminal resolver retained provisional grant")
				}
				failed, err := s.GetOperation(ctx, resolver.ID)
				if err != nil || failed.State != ops.RecoveryRequired {
					t.Fatal("unknown resolver not retained", err)
				}
				next, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: resolver.ID}, "requester", "wake-settlement")
				if err != nil {
					t.Fatal(err)
				}
				events, err = s.EventsAfter(ctx, next.ID, 0, 1024)
				if err != nil {
					t.Fatal(err)
				}
				host.stopped = true
				refused, err := executor.InspectResolution(ctx, next, failed, p, d, events)
				if err != nil || refused.Action != apply.RequireRecovery || starts != 1 {
					t.Fatal("unknown stopped start was retryable", err, refused.Action)
				}
				host.stopped = false
				assessment, err = executor.InspectResolution(ctx, next, failed, p, d, events)
				if err != nil || assessment.Action != apply.ResumeForward {
					t.Fatal("actual running unknown start not settled", err, assessment.Action)
				}
				if resolveErr := executor.Recover(ctx, assessment); resolveErr != nil {
					t.Fatal(resolveErr)
				}
			}
			if !unknown && err != nil || starts != 1 || !reflect.DeepEqual(host.checks, []bool{false, true}) {
				t.Fatal("frontend wake repeated or health missing", err, starts, host.checks)
			}
			release, err := s.CurrentRelease(ctx, p.App)
			if err != nil || release.ID != source.ID || release.Managed.Frontend.Unit != expected.Managed.Frontend.Unit {
				t.Fatal("wake changed original artifacts", err)
			}
			permission, err := s.ReadManagedFrontendPermit(ctx, p.App)
			if err != nil || permission.Pending || permission.PendingOperationID != "" || permission.Permit.OperationID != source.ID {
				t.Fatal("resolved wake did not replace temporary permission with joined serving proof", err)
			}
		})
	}
}

type failedManagedTerminal struct{ *Store }

func (s failedManagedTerminal) SetOperationState(ctx context.Context, id string, state ops.State) error {
	if state == ops.Succeeded {
		return errors.New("terminal journal interruption")
	}
	return s.Store.SetOperationState(ctx, id, state)
}
func TestManagedWakeCommittedReleaseResolutionSettlesTerminalWithoutAnotherStart(t *testing.T) {
	s, p, source, resolver, expected := managedRouteCommitFixture(t)
	ctx := context.Background()
	_, d, err := s.LoadPlan(ctx, p.Hash)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := s.Generation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := &managedCommitHost{state: s, owner: source.ID, stopped: true, route: caddy.State{Generation: expected.CaddyGeneration, Files: map[string]string{expected.CaddyFile.Name: expected.CaddyFile.Hash}}}
	facts := apply.Facts{Routing: host.route, Input: plan.Input{Desired: d, State: plan.BrineState{Target: p.Target, Generation: generation}}}
	facts.Input.Snapshot.Apps = target.Known([]target.App{{Name: p.App, Image: target.Known(target.Image{Digest: p.Image.Digest, Platform: p.Image.Platform}), AllocatedHostPort: target.Known(p.HostPort), QuadletUnits: target.Known([]target.Unit{expected.Managed.Frontend.Unit})}})
	starts := 0
	executor := apply.Executor{Journal: failedManagedTerminal{s}, Releases: managedCommitReleases{s}, Managed: host, Routes: host, Health: host, Facts: apply.FactsFunc(func(context.Context) (apply.Facts, error) { return facts, nil }), Systemd: &systemd.Fake{IsActiveFunc: func(context.Context, systemd.Unit) (bool, error) { return !host.stopped, nil }, StartFunc: func(context.Context, systemd.Unit) error { starts++; host.stopped = false; return nil }}, Podman: &podman.Fake{SecretExistsFunc: func(context.Context, podman.Name) (bool, error) { return true, nil }}}
	events, err := s.EventsAfter(ctx, resolver.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := executor.InspectResolution(ctx, resolver, source, p, d, events)
	if err != nil || assessment.Action != apply.ResumeForward {
		t.Fatal(err, assessment.Action)
	}
	if err := executor.Recover(ctx, assessment); err == nil {
		t.Fatal("terminal interruption missing")
	}
	failed, err := s.GetOperation(ctx, resolver.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State == ops.Committing {
		if err := s.SetOperationState(ctx, resolver.ID, ops.RecoveryRequired); err != nil {
			t.Fatal(err)
		}
		failed.State = ops.RecoveryRequired
	}
	next, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Resolve, PlanID: p.Hash, RecoveryOf: resolver.ID}, "requester", "wake-terminal-settlement")
	if err != nil {
		t.Fatal(err)
	}
	events, err = s.EventsAfter(ctx, next.ID, 0, 1024)
	if err != nil {
		t.Fatal(err)
	}
	executor.Journal = s
	assessment, err = executor.InspectResolution(ctx, next, failed, p, d, events)
	if err != nil || assessment.Action != apply.FinishSucceeded {
		t.Fatal("committed wake terminal was unreachable", err, assessment.Action)
	}
	if err := executor.Recover(ctx, assessment); err != nil || starts != 1 {
		t.Fatal("terminal settlement repeated start", err, starts)
	}
	if _, err := s.ReadManagedFrontendPermit(ctx, p.App); err != nil {
		t.Fatal("terminal settlement did not close serving proof", err)
	}
}
