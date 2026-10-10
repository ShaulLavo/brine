package apply

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func (e *Executor) inspectReplicaRevisionRecovery(ctx context.Context, op Operation, p plan.Plan, d policy.Desired, events []Event, r Recovery) (Recovery, error) {
	inspector, ok := e.PersistentData.(ReplicaRevisionRecovery)
	if !ok || p.ReplicaPrevious == nil || op.State.IsTerminal() || p.Hash != op.PlanID || !desiredMatches(p, d) || p.Kind != plan.Update || len(p.Conflicts) != 0 || e.Facts == nil || e.Releases == nil || e.Plans == nil {
		return r, nil
	}
	steps := []string{"preflight", "prepare_data", "commit"}
	index := 0
	var last stepPayload
	for _, event := range events {
		if event.Kind != "step" {
			continue
		}
		if json.Unmarshal(event.Payload, &last) != nil || index >= len(steps) || last.Step != steps[index] {
			return r, nil
		}
		r.Step = last.Step
		switch last.Outcome {
		case "completed":
			r.completed[last.Step] = true
			index++
		case "intent", "unknown":
		default:
			return r, nil
		}
	}
	if r.Step == "" {
		return r, nil
	}
	x := &execution{executor: e, id: op.ID, plan: p, desired: d, state: op.State, writerStartOwners: r.resolutionOwners}
	evidence, cancel := context.WithTimeout(ctx, e.effectTimeout())
	defer cancel()
	var err error
	x.facts, err = e.Facts.Read(evidence)
	if err != nil {
		return r, err
	}
	if !desiredMatches(p, x.facts.Input.Desired) || !reflect.DeepEqual(p.Target, x.facts.Input.Snapshot.Identity) {
		return r, nil
	}
	x.previous, x.hasPrevious, err = e.Releases.CurrentRelease(evidence, p.App)
	if err != nil || !x.hasPrevious {
		return r, err
	}
	if !releaseArtifactsObserved(x.facts, x.previous, p.App) || p.ReplicaPrevious.RoutingGeneration != x.facts.Routing.Generation || x.previous.CaddyFile.Hash != x.facts.Routing.Files[p.App+".caddy"] {
		return r, nil
	}
	owners := slices.Concat([]string{op.ID}, r.resolutionOwners)
	x.replicaOperation = owners[len(owners)-1]
	r.execution = x
	r.unknownBoundary = last.Outcome == "intent"
	if slices.Contains(owners, x.previous.ID) && x.previous.PlanID == p.Hash {
		if r.Step != "commit" || !reflect.DeepEqual(x.previous, replicaRelease(x.previous.ID, p.Hash, p)) {
			return r, nil
		}
		prepared, err := e.PersistentData.PersistentPrepared(evidence, x.replicaOperation, p, d)
		if err != nil || !prepared {
			return r, nil
		}
		r.Action = FinishSucceeded
		r.resolved = last.Outcome != "completed"
		return r, nil
	}
	if x.previous.ID != p.ReplicaPrevious.ReleaseID || !reflect.DeepEqual(x.previous, replicaRelease(x.previous.ID, x.previous.PlanID, p)) {
		return r, nil
	}
	if p.ObservedGeneration.Status != target.KnownStatus || p.ObservedGeneration.Value == nil || x.facts.Input.State.Generation != *p.ObservedGeneration.Value {
		return r, nil
	}
	oldPlan, oldDesired, err := e.Plans.LoadPlan(evidence, x.previous.PlanID)
	if err != nil {
		return r, err
	}
	if oldPlan.Hash != x.previous.PlanID || !desiredMatches(oldPlan, oldDesired) || !plan.CadenceOnly(d, oldDesired) {
		return r, nil
	}
	x.previousDesired = oldDesired
	fresh, err := plan.Build(x.facts.Input)
	if err != nil {
		return r, err
	}
	if fresh.Kind != plan.Update || fresh.Lifecycle != plan.ReviseReplica || fresh.DesiredHash != p.DesiredHash || fresh.ConfigHash != p.ConfigHash || fresh.HostPort != p.HostPort || !reflect.DeepEqual(fresh.ReplicaPrevious, p.ReplicaPrevious) || !reflect.DeepEqual(fresh.Image, p.Image) || !reflect.DeepEqual(fresh.Secrets, p.Secrets) || !reflect.DeepEqual(fresh.DataMounts, p.DataMounts) || !reflect.DeepEqual(fresh.DataCredentials, p.DataCredentials) {
		return r, nil
	}
	resumable, err := inspector.InspectReplicaRevision(evidence, x.replicaOperation, p, d)
	if err != nil || !resumable {
		return r, err
	}
	r.Action = ResumeForward
	// Replica preparation owns a separate cursor; an uncertain outer intent is
	// not marked complete until that cursor independently reaches active.
	if r.Step == "prepare_data" && last.Outcome != "completed" {
		r.resolved = false
	}
	if r.Step == "commit" && last.Outcome != "completed" {
		// The old release is still authoritative. Recommit only the same metadata.
		r.completed["commit"] = false
	}
	return r, nil
}
