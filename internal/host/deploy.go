package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

// DeploymentState distinguishes missing allocation from unknown live evidence.
// Unknown observations never become permission to initialize or skip a step.
func (s Service) DeploymentState(ctx context.Context, app spec.App) (_ dispatch.DeploymentState, retErr error) {
	out := dispatch.DeploymentState{Databases: []dispatch.DeploymentDatabase{}}
	if s.Requester == "" || s.Store == nil {
		return out, result.New(result.DispatchOperationRefused, nil)
	}
	lock, err := s.Store.AcquireHostLock(ctx)
	if err != nil {
		return out, err
	}
	defer func() { retErr = errors.Join(retErr, lock.Release()) }()
	facts, err := s.facts(ctx, app)
	if err != nil {
		return out, err
	}
	desired := facts.Input.Desired
	raw, err := desired.CanonicalBytes()
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(raw)
	out.ReviewHash = "sha256:" + hex.EncodeToString(digest[:])
	for _, declaration := range desired.Databases {
		item := dispatch.DeploymentDatabase{Name: string(declaration.Name)}
		var destination data.Destination
		for _, d := range desired.BackupDestinations {
			if d.Reference == declaration.BackupDestination {
				destination = d
			}
		}
		reserved, err := s.Store.ExistingDatabase(ctx, store.DataReservation{App: string(desired.Name), PolicyHash: desired.PolicyHash, Database: declaration, Destination: destination})
		if errors.Is(err, store.ErrNotFound) {
			out.Databases = append(out.Databases, item)
			continue
		}
		if err != nil {
			return out, err
		}
		proof, err := s.Store.ReadPreparationEvidence(ctx, reserved.Database.DatabaseID)
		if err != nil {
			return out, err
		}
		if proof == nil || proof.RootProof == nil || proof.MappingProof == nil {
			out.Databases = append(out.Databases, item)
			continue
		}
		observed := facts.Input.Snapshot.PersistentData
		if observed == nil || observed.Status != target.KnownStatus || observed.Value == nil {
			return out, result.New(result.Conflict, nil)
		}
		found := false
		for _, d := range *observed.Value {
			if d.Database.DatabaseID != reserved.Database.DatabaseID {
				continue
			}
			found = true
			if d.Schema.State == data.Unknown {
				return out, result.New(result.Conflict, nil)
			}
			item.Prepared = true
			item.Initialized = d.Schema.State == data.VerifiedSchema
			item.Credentials = d.Credentials.Status == target.KnownStatus && d.Credentials.Value != nil
			item.Fenced = d.Fenced
		}
		if !found {
			return out, result.New(result.Conflict, nil)
		}
		out.Databases = append(out.Databases, item)
	}
	return out, nil
}

func (s Service) DeploymentOperation(ctx context.Context, query dispatch.DeploymentOperationQuery) (dispatch.DeploymentOperation, error) {
	if s.Requester == "" || s.Store == nil {
		return dispatch.DeploymentOperation{}, result.New(result.DispatchOperationRefused, nil)
	}
	id, err := s.Store.FindDeploymentOperation(ctx, s.Requester, query.Kind, query.PlanID, query.Key)
	return dispatch.DeploymentOperation{OperationID: id}, err
}
