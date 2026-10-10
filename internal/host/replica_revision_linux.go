//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
)

func revisionID(operation string, b data.ReplicaBinding) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(operation+"\x00"+string(b.BindingID)+"\x00"+b.ConfigSHA256)))
}

func (p DataPreparation) prepareReplica(ctx context.Context, operation string, desired policy.Desired, artifact replication.Artifacts, after data.ReplicaBinding) error {
	host := replicaRotation{state: p.State, stateRoot: p.StateRoot, home: p.Home, services: p.Services, units: p.Units, permits: p.Permits}
	current, fenced, err := host.Inspect(ctx, after.BindingID)
	if err != nil {
		return err
	}
	record, err := p.State.ReadReplicaRevision(ctx, revisionID(operation, after))
	if errors.Is(err, store.ErrNotFound) {
		if !current.Committed {
			if fenced {
				return replication.ErrRestartUnknown
			}
			if err = p.Publisher.Publish(ctx, artifact); err != nil {
				return err
			}
			if err = p.State.CommitReplicaBinding(ctx, after); err != nil {
				return err
			}
			if err = host.Reload(ctx); err != nil {
				return err
			}
			orchestrator := apply.ReplicaOrchestrator{Permits: p.Permits, Services: p.Services, Locks: apply.KernelReplicaLocks{}}
			return orchestrator.Activate(ctx, apply.ReplicaActivation{ReplicaPermitRequest: replication.ReplicaPermitRequest{DatabaseID: string(after.DatabaseID), BindingID: string(after.BindingID), EpochID: string(after.EpochID), ConfigHash: replication.ConfigHash(artifact.Config)}, LifetimeLock: artifact.LifetimeLock})
		}
		if current == after {
			if err = verifyReplicaService(artifact); err != nil {
				return err
			}
			if err = host.Permit(ctx, after); err != nil {
				return err
			}
			running, err := host.Running(ctx, after)
			if err != nil || !running {
				return replication.ErrRestartUnknown
			}
			return nil
		}
		// Refuse foreign unit bytes before recording or stopping anything. This
		// evidence remains read-only under a fence; it does not admit an app writer.
		beforeArtifact, hash, err := host.artifact(ctx, current)
		if err != nil || hash != current.UnitSHA256 {
			return replication.ErrPermit
		}
		if err = verifyReplicaService(beforeArtifact); err != nil {
			return err
		}
		record = data.ReplicaRevision{ID: revisionID(operation, after), App: string(desired.Name), Before: current, After: after, Stage: data.RevisionStored}
		if err = p.State.WriteReplicaRevision(ctx, "", record); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if record.Validate() != nil || record.After != after || record.App != string(desired.Name) || current != record.Before && current != record.After {
		return replication.ErrPermit
	}
	if fenced {
		return replication.ErrRestartUnknown
	}
	advance := func(next data.RotationStage) error {
		previous := record.Stage
		record.Stage = next
		return p.State.WriteReplicaRevision(ctx, previous, record)
	}
	if record.Stage == data.RevisionStored {
		// Publication is exact-byte, append-only and read-back checked. A repeat of
		// an interrupted publish can only prove the same immutable revision.
		if err = p.Publisher.PublishConfig(ctx, artifact); err != nil {
			return replication.ErrRestartUnknown
		}
		if err = advance(data.RotationPrepared); err != nil {
			return replication.ErrRestartUnknown
		}
	}
	if err = replication.Restart(ctx, host, record.Before, record.After, record.Stage, advance, nil); err != nil {
		return replication.ErrRestartUnknown
	}
	return verifyReplicaService(artifact)
}

func verifyReplicaService(artifact replication.Artifacts) error {
	if err := data.VerifyRunnerFile(artifact.ServicePath); err != nil {
		return err
	}
	raw, err := os.ReadFile(artifact.ServicePath)
	if err != nil || string(raw) != string(artifact.Service) {
		return replication.ErrPermit
	}
	return nil
}

// InspectReplicaRevision checks a durable replica-only continuation. Unknown
// stop/start attempts must settle independently before any successor proceeds.
func (p DataPreparation) InspectReplicaRevision(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) (bool, error) {
	if planned.Lifecycle != plan.ReviseReplica || p.State == nil || p.Services == nil || p.Units == nil || p.Permits == nil {
		return false, replication.ErrPermit
	}
	artifacts, bindings, err := p.expected(ctx, planned, desired)
	if err != nil {
		return false, err
	}
	host := replicaRotation{state: p.State, stateRoot: p.StateRoot, home: p.Home, services: p.Services, units: p.Units, permits: p.Permits}
	for i, after := range bindings {
		current, fenced, err := host.Inspect(ctx, after.BindingID)
		if err != nil || fenced {
			return false, err
		}
		record, err := p.State.ReadReplicaRevision(ctx, revisionID(operation, after))
		if errors.Is(err, store.ErrNotFound) {
			artifact, hash, err := host.artifact(ctx, current)
			if err != nil || hash != current.UnitSHA256 {
				return false, replication.ErrPermit
			}
			if err = verifyReplicaService(artifact); err != nil {
				return false, err
			}
			continue
		}
		if err != nil || record.Validate() != nil || record.After != after || current != record.Before && current != after {
			return false, replication.ErrPermit
		}
		if record.Stage == data.RevisionStored {
			before, hash, err := host.artifact(ctx, record.Before)
			if err != nil || hash != record.Before.UnitSHA256 || current != record.Before {
				return false, replication.ErrPermit
			}
			if err = verifyReplicaService(before); err != nil {
				return false, err
			}
			continue
		}
		// Both configurations remain immutable. Replacement may have happened while
		// the stopped cursor's binding commit was interrupted.
		old, oldHash, err := host.artifact(ctx, record.Before)
		if err != nil || oldHash != record.Before.UnitSHA256 {
			return false, replication.ErrPermit
		}
		if _, hash, err := host.artifact(ctx, after); err != nil || hash != after.UnitSHA256 {
			return false, replication.ErrPermit
		}
		switch record.Stage {
		case data.RotationPrepared:
			if current != record.Before || verifyReplicaService(old) != nil {
				return false, replication.ErrPermit
			}
		case data.RotationStopIssued, data.RotationStopped:
			stopped, err := host.Stopped(ctx, record.Before)
			if err != nil || !stopped {
				return false, err
			}
			if verifyReplicaService(old) != nil && verifyReplicaService(artifacts[i]) != nil {
				return false, replication.ErrPermit
			}
		case data.RotationCommitted:
			if current != after || verifyReplicaService(artifacts[i]) != nil || host.Permit(ctx, after) != nil {
				return false, replication.ErrPermit
			}
		case data.RotationStartIssued, data.RotationActive:
			running, err := host.Running(ctx, after)
			if err != nil || !running || current != after || verifyReplicaService(artifacts[i]) != nil || host.Permit(ctx, after) != nil {
				return false, replication.ErrPermit
			}
		default:
			return false, replication.ErrPermit
		}
	}
	return true, nil
}
