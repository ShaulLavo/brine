package host

import (
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
)

type Runtime struct {
	Reconciler dispatch.ReconcileOperations
	Inventory  dispatch.Inventory
	Planner    dispatch.Planner
	Jobs       dispatch.JobOperations
	Runner     jobs.Runner
	Apps       dispatch.AppOperations
	Logs       dispatch.LogReader
	Diagnose   dispatch.DiagnosticReader
	Authorize  dispatch.Authorization
	close      func() error
}

func (r *Runtime) Close() error { return r.close() }
