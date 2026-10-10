package apply

import (
	"context"
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

// ReplicaRevisionRecovery inspects the replica's own durable stop/start cursor.
// It does not authorize replay of ordinary deployment preparation.
type ReplicaRevisionRecovery interface {
	InspectReplicaRevision(context.Context, string, plan.Plan, policy.Desired) (bool, error)
	ResumeReplicaRevision(context.Context, string, plan.Plan, policy.Desired) error
}

func (x *execution) reviseReplica(ctx context.Context, completed map[string]bool) error {
	if !completed["prepare_data"] {
		if err := x.step(ctx, "prepare_data", Preparing, "writer_permit_refused", x.preparePersistent); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	}
	if !completed["commit"] {
		if err := x.step(ctx, "commit", Committing, "commit_failed", x.commitReplicaMetadata); err != nil {
			return x.terminal(ctx, RecoveryRequired, err)
		}
	}
	return x.terminal(ctx, Succeeded, nil)
}

func (x *execution) commitReplicaMetadata(ctx context.Context) error {
	// A replica change must not conceal drift in the untouched app or route.
	facts, err := x.executor.Facts.Read(ctx)
	if err != nil || !desiredMatches(x.plan, facts.Input.Desired) || !releaseArtifactsObserved(facts, x.previous, x.plan.App) || x.plan.ReplicaPrevious == nil || facts.Routing.Generation != x.plan.ReplicaPrevious.RoutingGeneration || facts.Routing.Files[x.previous.CaddyFile.Name] != x.previous.CaddyFile.Hash {
		return &Error{Step: "commit", Code: "interrupted", Cause: err}
	}
	if x.plan.ReplicaPrevious == nil || x.previous.ID != x.plan.ReplicaPrevious.ReleaseID || !reflect.DeepEqual(x.previous, replicaRelease(x.previous.ID, x.previous.PlanID, x.plan)) {
		return &Error{Step: "commit", Code: "interrupted"}
	}
	candidate := replicaRelease(x.id, x.plan.Hash, x.plan)
	x.nextRelease = &candidate
	if err = x.executor.Releases.CommitRelease(ctx, x.plan.App, candidate); err == nil {
		return nil
	}
	current, exists, readErr := x.executor.Releases.CurrentRelease(ctx, x.plan.App)
	if readErr == nil && exists && reflect.DeepEqual(current, candidate) {
		return nil
	}
	return &Error{Step: "commit", Code: "interrupted", Cause: errors.Join(err, readErr)}
}

func replicaRelease(id, planID string, p plan.Plan) Release {
	return Release{ID: id, PlanID: planID, Image: p.Image, HostPort: p.HostPort, Secrets: p.Secrets, Units: p.ReplicaPrevious.Units, CaddyFile: p.ReplicaPrevious.CaddyFile, CaddyGeneration: p.ReplicaPrevious.CaddyGeneration}
}
