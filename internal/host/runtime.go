package host

import (
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/store"
)

type Runtime struct {
	previewStore      *store.Store
	Reconciler        dispatch.ReconcileOperations
	Inventory         dispatch.Inventory
	Planner           dispatch.Planner
	Jobs              dispatch.JobOperations
	Runner            jobs.Runner
	BackupCredentials dispatch.BackupCredentialOperations
	Config            dispatch.ConfigurationOperations
	Secrets           dispatch.SecretOperations
	Apps              dispatch.AppOperations
	Logs              dispatch.LogReader
	Diagnose          dispatch.DiagnosticReader
	Authorize         dispatch.Authorization
	close             func() error
}

func (r *Runtime) Close() error { return r.close() }
