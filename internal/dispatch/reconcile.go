package dispatch

import (
	"context"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type ReconcileArgs struct {
	DryRun bool `json:"dry_run"`
}
type ReconcileOperations interface {
	Reconcile(context.Context) (reconcile.Report, error)
	DryRun(context.Context) (reconcile.Report, error)
}

func decodeReconcile(raw json.RawMessage) (any, error) {
	fields, err := strictjson.Object(raw, "dry_run")
	if err != nil {
		return nil, err
	}
	dry, err := strictjson.Value[bool](fields["dry_run"])
	return ReconcileArgs{DryRun: dry}, err
}

type ReconcileJobs interface {
	Reconcile(context.Context) (jobs.Accepted, error)
}

func (s *Server) reconcileRequest(ctx context.Context, args ReconcileArgs) (any, error) {
	if args.DryRun {
		if s.Reconciler == nil {
			return nil, result.New(result.DependencyMissing, nil)
		}
		return s.Reconciler.DryRun(ctx)
	}
	jobs, ok := s.jobs.(ReconcileJobs)
	if !ok {
		return nil, result.New(result.DependencyMissing, nil)
	}
	return jobs.Reconcile(ctx)
}
