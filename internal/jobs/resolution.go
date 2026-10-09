package jobs

import (
	"context"
	"errors"

	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func (s Service) Resolve(ctx context.Context, sourceID, key string) (accepted Accepted, err error) {
	if s.Store == nil || s.Launcher == nil || s.Requester == "" {
		return Accepted{}, result.New(result.DependencyMissing, nil)
	}
	if !ValidID(sourceID) || !ValidID(key) {
		return Accepted{}, result.New(result.DispatchInvalidRequest, nil)
	}
	wait, cancel := context.WithTimeout(ctx, LaunchLockWaitTimeout)
	lock, err := s.Store.AcquireLaunchLock(wait)
	cancel()
	if err != nil {
		return accepted, err
	}
	defer func() { err = errors.Join(err, lock.Release()) }()
	source, err := s.Store.GetOperation(ctx, sourceID)
	if err != nil {
		return accepted, err
	}
	if source.State != ops.RecoveryRequired || source.Kind != ops.Deploy && source.Kind != ops.Resolve && source.Kind != ops.SecretSet {
		return accepted, result.New(result.Conflict, nil)
	}
	intent := ops.Intent{Kind: ops.Resolve, PlanID: source.PlanID, RecoveryOf: source.ID}
	if source.PlanID == "" {
		intent.App, intent.SecretRef = source.App, source.SecretRef
	}
	op, existing, err := s.Store.CreateOperation(ctx, intent, s.Requester, key)
	if err != nil {
		return accepted, err
	}
	return s.launch(ctx, op, existing)
}
