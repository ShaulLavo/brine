//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/store"
)

// ReconcileRevision settles existing effects without starting with old credentials.
// A fresh, scoped delivery cancels the cadence cursor only after this proof.
func (h replicaRotation) ReconcileRevision(ctx context.Context, fresh backupcredentials.Receipt) error {
	record, err := h.state.PendingReplicaRevision(ctx, data.ReplicaBindingID(fresh.Scope.Binding))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || record.Validate() != nil || fresh.Admit(h.currentTime(), time.Minute) != nil || record.App != fresh.Scope.App || string(record.Before.EpochID) != fresh.Scope.Epoch || string(record.Before.Destination.Reference) != fresh.Scope.Destination || record.Before.Destination.CredentialRef != fresh.Scope.CredentialRef || fresh.Version <= record.Before.CredentialVersion {
		return replication.ErrRestartUnknown
	}
	current, fenced, err := h.Inspect(ctx, record.Before.BindingID)
	if err != nil || fenced || current != record.Before && current != record.After {
		return replication.ErrRestartUnknown
	}
	stopped, stopErr := h.Stopped(ctx, current)
	if stopErr != nil || !stopped {
		stopped = false
		running, err := h.Running(ctx, current)
		if err != nil || !running {
			return replication.ErrRestartUnknown
		}
	}
	advance := func(stage data.RotationStage) error {
		previous := record.Stage
		record.Stage = stage
		return h.state.WriteReplicaRevision(ctx, previous, record)
	}
	if record.Stage == data.RotationStopIssued && stopped {
		if err := advance(data.RotationStopped); err != nil {
			return err
		}
	}
	if record.Stage == data.RotationStopped {
		if !stopped {
			return replication.ErrRestartUnknown
		}
		release, err := h.Acquire(ctx, record.Before)
		if err != nil {
			return replication.ErrRestartUnknown
		}
		err = h.Commit(ctx, record.Before, record.After)
		if err == nil {
			err = advance(data.RotationCommitted)
		}
		releaseErr := release()
		if err != nil || releaseErr != nil {
			return replication.ErrRestartUnknown
		}
		current = record.After
	}
	switch record.Stage {
	case data.RevisionStored, data.RotationPrepared, data.RotationStopIssued:
		if current != record.Before {
			return replication.ErrRestartUnknown
		}
	case data.RotationCommitted, data.RotationStartIssued:
		if current != record.After {
			return replication.ErrRestartUnknown
		}
	default:
		return replication.ErrRestartUnknown
	}
	artifact, hash, err := h.artifact(ctx, current)
	if err != nil || hash != current.UnitSHA256 || verifyReplicaService(artifact) != nil {
		return replication.ErrRestartUnknown
	}
	return h.state.CancelReplicaRevision(ctx, record, fresh.Version)
}
