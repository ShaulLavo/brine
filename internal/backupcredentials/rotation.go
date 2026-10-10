package backupcredentials

import (
	"context"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

var ErrActivationUnknown = errors.New("backup credential activation requires reconciliation")
var ErrRemoteAccess = errors.New("backup credential remote access was not verified")
var ErrRotationNotFound = errors.New("backup credential activation record not found")

type RotationJournal interface {
	ReadRotation(context.Context, string) (data.CredentialRotation, error)
	WriteRotation(context.Context, data.RotationStage, data.CredentialRotation) error
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
}

// Activate runs under the delivery host lock. Stop/start attempts are persisted
// before issuing them. An uncertain attempt is inspected, never issued twice.
func (r Rotator) Activate(ctx context.Context, receipt Receipt) (Receipt, error) {
	if !receipt.Valid() || r.Journal == nil || r.Host == nil {
		return Receipt{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
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
	if err := receipt.Admit(time.Now().UTC(), time.Minute); err != nil {
		return Receipt{}, err
	}
	record, err := r.Journal.ReadRotation(ctx, receipt.PlanID)
	if errors.Is(err, ErrRotationNotFound) {
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
		if err := r.Journal.WriteRotation(ctx, "", record); err != nil {
			return Receipt{}, ErrActivationUnknown
		}
	} else if err != nil {
		return Receipt{}, ErrActivationUnknown
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
	if record.Stage == data.RotationPrepared {
		if err := advance(data.RotationStopIssued); err != nil {
			return Receipt{}, err
		}
		// Even a failed Stop may have taken effect. Independently inspect below.
		_ = r.Host.Stop(ctx, record.Before)
	}
	if record.Stage == data.RotationStopIssued {
		stopped, err := r.Host.Stopped(ctx, record.Before)
		if err != nil || !stopped {
			return Receipt{}, ErrActivationUnknown
		}
		if err := advance(data.RotationStopped); err != nil {
			return Receipt{}, err
		}
	}
	if record.Stage == data.RotationStopped {
		release, err := r.Host.Acquire(ctx, record.Before)
		if err != nil {
			return Receipt{}, ErrActivationUnknown
		}
		commitErr := func() error {
			observed, fenced, err := r.Host.Inspect(ctx, record.Before.BindingID)
			if err != nil || fenced || observed != record.Before && observed != record.After {
				return ErrActivationUnknown
			}
			if err := r.Host.Commit(ctx, record.Before, record.After); err != nil {
				return ErrActivationUnknown
			}
			return advance(data.RotationCommitted)
		}()
		releaseErr := release()
		if commitErr != nil || releaseErr != nil {
			return Receipt{}, ErrActivationUnknown
		}
	}
	if record.Stage == data.RotationCommitted {
		observed, fenced, err := r.Host.Inspect(ctx, record.After.BindingID)
		if err != nil || fenced || observed != record.After {
			return Receipt{}, ErrActivationUnknown
		}
		if r.Host.Reload(ctx) != nil || r.Host.Permit(ctx, record.After) != nil {
			return Receipt{}, ErrActivationUnknown
		}
		if err := advance(data.RotationStartIssued); err != nil {
			return Receipt{}, err
		}
		// Do not compensate or repeat an uncertain start. Running is independent.
		_ = r.Host.Start(ctx, record.After)
	}
	if record.Stage != data.RotationStartIssued && record.Stage != data.RotationActive && record.Stage != data.RotationVerified {
		return Receipt{}, ErrActivationUnknown
	}
	running, err := r.Host.Running(ctx, record.After)
	if err != nil || !running {
		return Receipt{}, ErrActivationUnknown
	}
	if record.Stage == data.RotationStartIssued {
		if err := advance(data.RotationActive); err != nil {
			return Receipt{}, err
		}
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
