package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

const deploymentCheckpointLockWait = 5 * time.Second

// DeploymentCheckpoint never treats a private client checkpoint as authority.
// LoadPlan/LoadInitPlan verify retained hashes; this binds their actual contents
// to the requested app, pinned host identity and current normalized desired input.
func (s Service) DeploymentCheckpoint(ctx context.Context, app spec.App, q dispatch.DeploymentCheckpointArgs) (_ dispatch.DeploymentCheckpoint, retErr error) {
	out := dispatch.DeploymentCheckpoint{PlanID: q.PlanID}
	if s.Requester == "" || s.Store == nil {
		return out, result.New(result.DispatchOperationRefused, nil)
	}
	// Terminal status can precede the runner's deferred lock release. Allow
	// that handoff, but return a typed conflict before the SSH call expires.
	wait, cancel := context.WithTimeout(ctx, deploymentCheckpointLockWait)
	lock, err := s.Store.AcquireHostLock(wait)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return out, result.New(result.Conflict, err)
	}
	if err != nil {
		return out, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	facts, err := s.facts(ctx, app)
	if err != nil {
		return out, err
	}
	out.ReviewHash, err = plan.DeploymentReviewHash(facts.Input)
	if err != nil {
		return out, err
	}
	desired, err := facts.Input.Desired.CanonicalBytes()
	if err != nil {
		return out, err
	}
	checkRelease := func(id string, preparation bool) (*dispatch.Planned, error) {
		p, d, err := s.Store.LoadPlan(ctx, id)
		if err != nil {
			return nil, err
		}
		raw, err := d.CanonicalBytes()
		if err != nil {
			return nil, err
		}
		if p.App != string(app.Name) || p.Target != facts.Input.Snapshot.Identity || !bytes.Equal(raw, desired) || (p.Lifecycle == plan.PrepareData) != preparation || (!preparation && p.Lifecycle != "") {
			return nil, result.New(result.Conflict, nil)
		}
		return &dispatch.Planned{PlanID: id, Kind: p.Kind, Diff: p.Diff, SchemaArtifacts: p.SchemaArtifacts, Conflicts: p.Conflicts}, nil
	}
	identity, err := json.Marshal(facts.Input.Snapshot.Identity)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(identity)
	targetHash := "sha256:" + hex.EncodeToString(digest[:])
	switch q.Step {
	case "preparation", "release":
		out.Planned, err = checkRelease(q.PlanID, q.Step == "preparation")
		if err != nil {
			return out, err
		}
	case "initialization":
		p, err := s.Store.LoadInitPlan(ctx, q.PlanID)
		if err != nil || p.Requester.String() != s.Requester || p.Request.App != string(app.Name) || p.Database.Name != data.DatabaseName(q.Database) || p.TargetHash != targetHash || p.PolicyHash != facts.Input.Desired.PolicyHash || !slices.Contains(app.SchemaArtifacts, data.SchemaArtifact{Definition: p.Definition, Digest: p.Request.Artifact}) {
			return out, result.New(result.Conflict, nil)
		}
		if _, err = checkRelease(p.Request.FirstReleasePlan, true); err != nil {
			return out, err
		}
	case "credentials":
		id := sha256.Sum256([]byte("plan\x00" + q.PlanID))
		record, err := s.Store.LoadCredentialRecord(ctx, hex.EncodeToString(id[:16]))
		if err != nil || record.Kind != "plan" || record.PlanID != q.PlanID || record.Requester != s.Requester || record.App != string(app.Name) || record.TargetHash != targetHash || record.PolicyHash != facts.Input.Desired.PolicyHash {
			return out, result.New(result.Conflict, nil)
		}
		scopes, err := s.Store.ReadCredentialScopes(ctx, string(app.Name))
		if err != nil {
			return out, err
		}
		matched := false
		for _, scope := range scopes {
			if string(scope.Database.Name) == q.Database && scope.Database.ReplicaBindingID == record.BindingID && scope.Replica.EpochID == record.EpochID && scope.Replica.Destination.Reference == record.Destination && scope.Replica.Destination.CredentialRef == record.CredentialRef {
				matched = true
			}
		}
		if !matched {
			return out, result.New(result.Conflict, nil)
		}
	default:
		return out, result.New(result.InvalidUsage, nil)
	}
	return out, nil
}
