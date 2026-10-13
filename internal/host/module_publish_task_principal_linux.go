//go:build linux

package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/ops"
)

func (o modulePublications) forTask(ctx context.Context, op ops.Operation) (modulePublications, error) {
	if o.service.Store == nil {
		return modulePublications{}, datapublish.ErrRecord
	}
	review, err := o.service.Store.ReadPublicationReview(ctx, op.SecretRef)
	if err != nil {
		return modulePublications{}, datapublish.ErrRecord
	}
	o.service, err = publicationTaskService(o.service, op, review.Plan())
	if err != nil || o.checkReviewedPlan(ctx, op.App, op.SecretRef) != nil {
		return modulePublications{}, datapublish.ErrRecord
	}
	return o, nil
}

func publicationTaskService(service Service, op ops.Operation, reviewed datapublish.Plan) (Service, error) {
	if op.Kind != ops.ModulePublishApply && op.Kind != ops.ModulePublishResolve || op.State != ops.Preflight ||
		!reviewed.Requester().Valid() || reviewed.ID() != op.SecretRef || reviewed.Facts().App != op.App || reviewed.Requester().String() != op.Requester {
		return Service{}, datapublish.ErrRecord
	}
	service.Requester = reviewed.Requester().String()
	return service, nil
}
