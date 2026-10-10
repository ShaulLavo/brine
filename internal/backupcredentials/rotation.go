package backupcredentials

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/replication"
)

var ErrActivationUnknown = errors.New("backup credential activation requires reconciliation")
var ErrRemoteAccess = errors.New("backup credential remote access was not verified")
var ErrRotationNotFound = errors.New("backup credential activation record not found")

type RotationJournal interface {
	ReadRotation(context.Context, string) (data.CredentialRotation, error)
	WriteRotation(context.Context, data.RotationStage, data.CredentialRotation) error
	PendingRotation(context.Context, data.ReplicaBindingID) (data.CredentialRotation, error)
	SupersedeRotation(context.Context, data.CredentialRotation, data.CredentialRotation) error
}
type RotationHost interface {
	Inspect(context.Context, data.ReplicaBindingID) (data.ReplicaBinding, bool, error)
	Prepare(context.Context, Receipt, data.ReplicaBinding) (data.ReplicaBinding, error)
	Stop(context.Context, data.ReplicaBinding) error
	Stopped(context.Context, data.ReplicaBinding) (bool, error)
	Acquire(context.Context, data.ReplicaBinding) (func() error, error)
	Commit(context.Context, data.ReplicaBinding, data.ReplicaBinding) error
	Reload(context.Context) error
	Permit(context.Context, data.ReplicaBinding) error
	Start(context.Context, data.ReplicaBinding) error
	Running(context.Context, data.ReplicaBinding) (bool, error)
	VerifyRemote(context.Context, Receipt, data.ReplicaBinding) error
}
type Rotator struct {
	Journal RotationJournal
	Host    RotationHost
	Now     func() time.Time
}

// Activate runs under the delivery host lock. Stop/start attempts are persisted
// before issuing them. An uncertain attempt is inspected, never issued twice.
func (r Rotator) Activate(ctx context.Context, receipt Receipt) (Receipt, error) {
	if !receipt.Valid() || r.Journal == nil || r.Host == nil {
		return Receipt{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	record, journalErr := r.Journal.ReadRotation(ctx, receipt.PlanID)
	if journalErr != nil && !errors.Is(journalErr, ErrRotationNotFound) {
		return Receipt{}, ErrActivationUnknown
	}
	var pending *data.CredentialRotation
	if errors.Is(journalErr, ErrRotationNotFound) {
		old, err := r.Journal.PendingRotation(ctx, data.ReplicaBindingID(receipt.Scope.Binding))
		if err == nil {
			pending = &old
		} else if !errors.Is(err, ErrRotationNotFound) {
			return Receipt{}, ErrActivationUnknown
		}
	}
	current, fenced, err := r.Host.Inspect(ctx, data.ReplicaBindingID(receipt.Scope.Binding))
	if err != nil {
		return Receipt{}, ErrActivationUnknown
	}
	if fenced {
		receipt.ActivationStatus = "fenced"
		return receipt, nil
	}
	if !current.Committed {
		receipt.ActivationStatus = "not_committed"
		return receipt, nil
	}
	if string(current.EpochID) != receipt.Scope.Epoch || current.Destination.CredentialRef != receipt.Scope.CredentialRef || string(current.Destination.Reference) != receipt.Scope.Destination {
		return Receipt{}, ErrStale
	}
	if journalErr == nil && record.Expired(r.now()) {
		if record.Validate() != nil || record.PlanID != receipt.PlanID || record.App != receipt.Scope.App || record.After.CredentialVersion != receipt.Version {
			return Receipt{}, ErrStale
		}
		if record.Stage == data.RotationSuperseded {
			return Receipt{}, ErrStale
		}
		if _, err := r.reconcileExpired(ctx, record); err != nil {
			return Receipt{}, err
		}
		return Receipt{}, ErrExpired
	}
	if pending != nil {
		if pending.Validate() != nil || !pending.Expired(r.now()) || pending.App != receipt.Scope.App || pending.After.CredentialVersion >= receipt.Version {
			return Receipt{}, ErrActivationUnknown
		}
		reconciled, err := r.reconcileExpired(ctx, *pending)
		if err != nil {
			return Receipt{}, err
		}
		pending = &reconciled
		current, fenced, err = r.Host.Inspect(ctx, data.ReplicaBindingID(receipt.Scope.Binding))
		if err != nil || fenced {
			return Receipt{}, ErrActivationUnknown
		}
	}
	if err := receipt.Admit(r.now(), time.Minute); err != nil {
		return Receipt{}, err
	}
	if errors.Is(journalErr, ErrRotationNotFound) {
		if current.CredentialVersion > receipt.Version {
			return Receipt{}, ErrStale
		}
		// Initial storage may have been followed by a successful deploy. Prove the
		// selected version is already active without manufacturing a new rotation.
		if current.CredentialVersion == receipt.Version {
			expected, err := r.Host.Prepare(ctx, receipt, current)
			if err != nil || expected != current || r.Host.Permit(ctx, current) != nil {
				return Receipt{}, ErrActivationUnknown
			}
			running, err := r.Host.Running(ctx, current)
			if err != nil || !running {
				return Receipt{}, ErrActivationUnknown
			}
			if r.Host.VerifyRemote(ctx, receipt, current) != nil {
				return Receipt{}, ErrRemoteAccess
			}
			receipt.Activated = true
			receipt.ActivationStatus = "verified"
			return receipt, nil
		}
		next, err := r.Host.Prepare(ctx, receipt, current)
		if err != nil {
			return Receipt{}, ErrActivationUnknown
		}
		record = data.CredentialRotation{PlanID: receipt.PlanID, App: receipt.Scope.App, Before: current, After: next, Stage: data.RotationPrepared}
		if receipt.ExpiresAt != nil {
			record.ExpiresAt = receipt.ExpiresAt.Format(time.RFC3339Nano)
		}
		var writeErr error
		if pending != nil {
			writeErr = r.Journal.SupersedeRotation(ctx, *pending, record)
		} else {
			writeErr = r.Journal.WriteRotation(ctx, "", record)
		}
		if writeErr != nil {
			return Receipt{}, ErrActivationUnknown
		}
	}
	if record.Validate() != nil || record.PlanID != receipt.PlanID || record.App != receipt.Scope.App || record.After.CredentialVersion != receipt.Version || current != record.Before && current != record.After {
		return Receipt{}, ErrStale
	}
	advance := func(stage data.RotationStage) error {
		previous := record.Stage
		record.Stage = stage
		if err := r.Journal.WriteRotation(ctx, previous, record); err != nil {
			return ErrActivationUnknown
		}
		return nil
	}
	if err := replication.Restart(ctx, r.Host, record.Before, record.After, record.Stage, advance, func() error { return receipt.Admit(r.now(), 0) }); err != nil {
		if errors.Is(err, ErrExpired) {
			return Receipt{}, err
		}
		return Receipt{}, ErrActivationUnknown
	}
	if err := r.Host.VerifyRemote(ctx, receipt, record.After); err != nil {
		return Receipt{}, ErrRemoteAccess
	}
	if record.Stage == data.RotationActive {
		if err := advance(data.RotationVerified); err != nil {
			return Receipt{}, err
		}
	}
	receipt.Activated = true
	receipt.ActivationStatus = "verified"
	return receipt, nil
}

func (r Rotator) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// reconcileExpired settles durable effects without issuing stop/start. A split
// unit/binding commit is completed under the lifetime lock before supersession.
func (r Rotator) reconcileExpired(ctx context.Context, record data.CredentialRotation) (data.CredentialRotation, error) {
	current, fenced, err := r.Host.Inspect(ctx, record.Before.BindingID)
	if err != nil || fenced || current != record.Before && current != record.After {
		return record, ErrActivationUnknown
	}
	stopped, err := r.Host.Stopped(ctx, current)
	if err != nil {
		return record, ErrActivationUnknown
	}
	running := false
	if !stopped {
		running, err = r.Host.Running(ctx, current)
		if err != nil || !running {
			return record, ErrActivationUnknown
		}
	}
	advance := func(stage data.RotationStage) error {
		prior := record.Stage
		record.Stage = stage
		if err := r.Journal.WriteRotation(ctx, prior, record); err != nil {
			return ErrActivationUnknown
		}
		return nil
	}
	if record.Stage == data.RotationPrepared {
		if current != record.Before {
			return record, ErrActivationUnknown
		}
		return record, nil
	}
	if record.Stage == data.RotationStopIssued {
		if !stopped {
			if current != record.Before {
				return record, ErrActivationUnknown
			}
			return record, nil
		}
		if err := advance(data.RotationStopped); err != nil {
			return record, err
		}
	}
	if record.Stage == data.RotationStopped {
		if !stopped {
			return record, ErrActivationUnknown
		}
		release, err := r.Host.Acquire(ctx, record.Before)
		if err != nil {
			return record, ErrActivationUnknown
		}
		err = r.Host.Commit(ctx, record.Before, record.After)
		if err == nil {
			err = advance(data.RotationCommitted)
		}
		releaseErr := release()
		if err != nil || releaseErr != nil {
			return record, ErrActivationUnknown
		}
		current = record.After
	}
	if current != record.After {
		return record, ErrActivationUnknown
	}
	// Keep an expired start intent pending even when independently proved running.
	// Only atomic fresh supersession closes it, preserving the replacement link.
	switch record.Stage {
	case data.RotationCommitted, data.RotationStartIssued, data.RotationActive, data.RotationVerified:
		return record, nil
	default:
		return record, ErrActivationUnknown
	}
}
