package datainit

import (
	"context"
	"github.com/ShaulLavo/brine/internal/data"
	"time"
)

// Reconcile only observes a retained attempt. It never uploads, creates a live
// file, or runs schema statements. Unknown or changed evidence keeps the fence.
func (s Service) Reconcile(ctx context.Context, app, id string) (Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	release, err := s.guard(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer release()
	p, err := s.Journal.LoadInitPlan(ctx, id)
	if err != nil || !p.Valid() || p.Requester != s.Requester || p.Request.App != app {
		return Operation{}, ErrRefused
	}
	old, exists, err := s.Journal.ReadInitOperation(ctx, id)
	if err != nil || !exists {
		return old, ErrRecovery
	}
	if old.State == "succeeded" || old.State == "not_initialized" {
		return old, nil
	}
	f, err := s.Facts(ctx, p.Request)
	if err != nil || f.Plan.Database != p.Database || f.Plan.ReplicaEpoch != p.ReplicaEpoch || f.Plan.Definition != p.Definition {
		return old, ErrRecovery
	}
	switch f.Observation.State {
	case data.AllocatedEmpty, data.VerifiedEmpty:
		// Allocation evidence or an independently observed empty catalog proves the
		// atomic schema transaction did not commit. The attempt remains consumed.
		return s.Journal.SetInitState(ctx, old, "not_initialized")
	case data.VerifiedSchema:
		point, err := s.Journal.LoadInitRestorePoint(ctx, old)
		if err != nil || !point.Admits(p, point.Receipt.ObservedAt) || point.Receipt.OperationID != old.ID+"-empty-verify" || f.Observation.Marker != p.Definition.Marker || f.Observation.CatalogSHA256 != p.Definition.CatalogSHA256 {
			return old, ErrRecovery
		}
		return s.Journal.SetInitState(ctx, old, "succeeded")
	default:
		return old, ErrRecovery
	}
}
