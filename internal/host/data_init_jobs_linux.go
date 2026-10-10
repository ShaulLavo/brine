//go:build linux

package host

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/ops"
)

// initializationOperations accepts reference-only work; it never initializes a
// database inside the dispatcher's short-lived observer context.
type initializationOperations struct {
	engine datainit.Service
	tasks  jobs.Service
}

func (s initializationOperations) Plan(ctx context.Context, request datainit.Request) (datainit.Plan, error) {
	return s.engine.Plan(ctx, request)
}
func (s initializationOperations) Apply(ctx context.Context, app, reference string) (jobs.Accepted, error) {
	if s.engine.Authorize == nil || s.engine.Journal == nil || !s.engine.Requester.Valid() || s.tasks.Requester != s.engine.Requester.String() {
		return jobs.Accepted{}, datainit.ErrRefused
	}
	if err := s.engine.Authorize(ctx); err != nil {
		return jobs.Accepted{}, err
	}
	p, err := s.engine.Journal.LoadInitPlan(ctx, reference)
	if err != nil || !p.Valid() || p.ID != reference || p.Request.App != app || p.Requester != s.engine.Requester {
		return jobs.Accepted{}, datainit.ErrRefused
	}
	// One accepted task per immutable plan and server-established requester. A
	// lost acceptance response recovers the same job, never a second launch.
	return s.tasks.Submit(ctx, ops.Intent{Kind: ops.DataInitApply, App: app, SecretRef: reference}, "data-init-"+strings.TrimPrefix(reference, "sha256:"))
}

func (s initializationOperations) Operation(ctx context.Context, id string, cursor uint64) (jobs.Status, error) {
	return s.tasks.Operation(ctx, id, cursor)
}

func initializationTaskEngine(ctx context.Context, service Service, stateRoot string, job ops.Operation) (datainit.Service, error) {
	if job.Kind != ops.DataInitApply || service.Store == nil {
		return datainit.Service{}, datainit.ErrRefused
	}
	p, err := service.Store.LoadInitPlan(ctx, job.SecretRef)
	if err != nil || !p.Valid() || p.ID != job.SecretRef || p.Request.App != job.App || p.Requester.String() != job.Requester {
		return datainit.Service{}, datainit.ErrRefused
	}
	// Only the matching immutable plan and server-established job identity may
	// reconstruct authority. Neither a request nor a run-op argument selects it.
	runService := service
	runService.Requester = p.Requester.String()
	return dataInitializationService(runService, stateRoot, p.Requester.IsLocalOperator()), nil
}

func initializationTask(service Service, stateRoot string) func(context.Context, ops.Operation) (json.RawMessage, error) {
	return func(ctx context.Context, job ops.Operation) (json.RawMessage, error) {
		engine, err := initializationTaskEngine(ctx, service, stateRoot, job)
		if err != nil {
			return nil, dispatch.DataInitializationFailure(err)
		}
		operation, err := engine.Apply(ctx, job.App, job.SecretRef)
		if err != nil {
			return nil, dispatch.DataInitializationFailure(err)
		}
		return json.Marshal(operation)
	}
}
