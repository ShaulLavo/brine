//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
)

func (o modulePublications) Apply(ctx context.Context, args dispatch.ModuleApplyArgs) (jobs.Accepted, error) {
	if !args.Valid() || o.service.Authorize(ctx, dispatch.Mutating) != nil {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	if err := o.checkReviewedPlan(ctx, args.App, args.PlanID); err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.ModulePublishApply, App: args.App, SecretRef: args.PlanID}, args.IdempotencyKey)
}

func (o modulePublications) Inspect(ctx context.Context, args dispatch.ModuleInspectArgs) (datapublish.Inspection, error) {
	if !args.Valid() || o.service.Authorize(ctx, dispatch.ReadOnly) != nil {
		return datapublish.Inspection{}, result.New(result.InvalidUsage, nil)
	}
	return o.service.Store.PublicationInspection(ctx, args.PlanID, o.service.Requester)
}

func (o modulePublications) Resolve(ctx context.Context, args dispatch.ModuleResolveArgs) (jobs.Accepted, error) {
	if !args.Valid() || o.service.Authorize(ctx, dispatch.Mutating) != nil {
		return jobs.Accepted{}, result.New(result.InvalidUsage, nil)
	}
	if err := o.checkReviewedPlan(ctx, args.App, args.PlanID); err != nil {
		return jobs.Accepted{}, err
	}
	return o.jobs.Submit(ctx, ops.Intent{Kind: ops.ModulePublishResolve, App: args.App, SecretRef: args.PlanID, RecoveryOf: args.OriginalOperationID}, args.IdempotencyKey)
}

func (o modulePublications) checkReviewedPlan(ctx context.Context, app, id string) error {
	review, err := o.service.Store.ReadPublicationReview(ctx, id)
	pol, policyErr := o.service.Policy.Load(ctx)
	if err != nil || policyErr != nil || review.Plan().Facts().App != app || review.Plan().Requester().String() != o.service.Requester || pol.Hash() != "sha256:"+review.Plan().Facts().PolicySHA256 {
		return result.New(result.PolicyRefused, nil)
	}
	if _, err = o.service.Store.PublicationInspection(ctx, id, o.service.Requester); err != nil {
		return err
	}
	return nil
}
