package apply

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

type ManagedRecoveryRoutes interface {
	InspectManagedLoaded(context.Context, caddy.State, plan.Plan, policy.Desired) error
}

func (e *Executor) inspectManagedInitialized(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	if e.Managed == nil || e.Facts == nil || e.Releases == nil || p.Hash != op.PlanID || !desiredMatches(p, d) || len(p.Conflicts) != 0 || op.State.IsTerminal() {
		return r, nil
	}
	if p.Lifecycle != "" {
		return e.inspectManagedLifecycle(ctx, op, p, d, events, r)
	}
	if cursor, valid := ops.ManagedCreateRecoveryCursor(events, p); valid {
		last := cursor.Boundary
		_, exists, err := e.Releases.CurrentRelease(ctx, p.App)
		if err != nil {
			return r, err
		}
		if !exists && op.Kind == ops.Deploy && last.Step == "managed_commit" {
			return r, nil
		}
		if !exists && (last.Step == "managed_route_receipt" || last.Step == "managed_route_check" || last.Outcome != "completed") {
			return e.inspectManagedRoute(ctx, op, p, d, cursor, r)
		}
	}
	if ops.InterruptedFrontendAttachment(events, p) {
		return e.inspectManagedAttachment(ctx, op, p, d, events, r)
	}
	last, valid := managedInitializedPrefix(events, p)
	if cursor, exact := ops.ManagedCreateRecoveryCursor(events, p); exact {
		last, valid = cursor.Boundary, true
	}
	r.Step = last.Step
	if !valid || last.Step != "managed_commit" || p.Lifecycle != "" {
		return r, nil
	}
	routes, ok := e.Routes.(ManagedRecoveryRoutes)
	if !ok {
		return r, nil
	}
	evidence, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	release, exists, err := e.Releases.CurrentRelease(evidence, p.App)
	if err != nil {
		return r, err
	}
	if !exists || release.PlanID != p.Hash || release.Managed == nil || release.Managed.RuntimeSHA256 != p.Managed.RuntimeSHA256 || release.ID != op.ID && !slices.Contains(r.resolutionOwners, release.ID) {
		return r, nil
	}
	facts, err := e.Facts.Read(evidence)
	if err != nil {
		return r, err
	}
	if err := e.Managed.CompletedManaged(evidence, release.ID, p, d); err != nil {
		return r, nil
	}
	if p.Managed.Frontend != nil && !managedRecoveryFrontendImage(facts, p) {
		return r, nil
	}
	if facts.Routing.Files[release.CaddyFile.Name] != release.CaddyFile.Hash || facts.Routing.Generation != release.CaddyGeneration || routes.InspectManagedLoaded(evidence, facts.Routing, p, d) != nil || evidence.Err() != nil {
		return r, nil
	}
	r.Action, r.resolutionState = FinishSucceeded, Committing
	r.resolved = last.Outcome != "completed"
	r.unknownBoundary = last.Outcome == "intent"
	r.execution = &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State}
	return r, nil
}
func managedInitializedPrefix(events []Event, p plan.Plan) (ops.StepPayload, bool) {
	var last ops.StepPayload
	allowed := map[string]int{"preflight": 0, "managed_desired": 1, "engine_stage": 2, "frontend_pull": 3, "frontend_verify": 4, "frontend_secrets": 5, "frontend_stage": 6, "frontend_stop": 7, "engine_install": 8, "engine_reload": 9, "engine_start": 10, "engine_check": 11, "frontend_install": 12, "frontend_reload": 13, "frontend_bind": 14, "frontend_start": 15, "frontend_check": 16, "frontend_remove": 17, "frontend_remove_reload": 18, "managed_route": 19, "managed_route_receipt": 20, "managed_route_check": 21, "managed_commit": 22}
	required := []string{"preflight", "managed_desired", "engine_stage", "engine_check", "managed_route", "managed_route_receipt", "managed_route_check"}
	if p.Managed.Frontend != nil {
		required = append(required, "frontend_pull", "frontend_verify", "frontend_secrets", "frontend_stage", "frontend_install", "frontend_reload", "frontend_bind", "frontend_start", "frontend_check")
	}
	if managedChange(p, plan.StartManagedEngine) {
		required = append(required, "engine_install", "engine_reload", "engine_start")
	}
	if managedChange(p, plan.RemoveManagedFrontend) {
		required = append(required, "frontend_remove", "frontend_remove_reload")
	}
	completed := map[string]bool{}
	previous := -1
	group := ""
	outcome := ""
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if ops.ValidateEvent(event) != nil || json.Unmarshal(event.Payload, &last) != nil {
			return last, false
		}
		rank, ok := allowed[last.Step]
		if !ok || rank < previous {
			return last, false
		}
		if last.Step != group {
			if group != "" && outcome != "completed" || last.Outcome != "intent" {
				return last, false
			}
			group, outcome, previous = last.Step, "", rank
		}
		if !managedOutcomeFollows(outcome, last.Outcome) {
			return last, false
		}
		outcome = last.Outcome
		completed[last.Step] = outcome == "completed"
	}
	for _, step := range required {
		if !completed[step] {
			return last, false
		}
	}
	return last, previous == 22 && (outcome == "intent" || outcome == "unknown" || outcome == "completed")
}
func managedOutcomeFollows(before, after string) bool {
	return before == "" && after == "intent" || before == "intent" && (after == "unknown" || after == "completed") || before == "unknown" && after == "completed"
}

func managedRecoveryFrontendImage(facts Facts, p plan.Plan) bool {
	apps := facts.Input.Snapshot.Apps
	if apps.Status != target.KnownStatus || apps.Value == nil {
		return false
	}
	for _, app := range *apps.Value {
		if app.Name != p.App {
			continue
		}
		return app.Image.Status == target.KnownStatus && app.Image.Value != nil && app.Image.Value.Digest == p.Managed.Frontend.Image.Digest && app.Image.Value.Platform == p.Managed.Frontend.Image.Platform && app.AllocatedHostPort.Status == target.KnownStatus && app.AllocatedHostPort.Value != nil && *app.AllocatedHostPort.Value == p.Managed.Frontend.Port
	}
	return false
}
