package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/store"
)

type Runtime struct {
	ModulePublications dispatch.ModulePublicationOperations
	Archives           archive.Service
	ArchiveMaintenance archive.MaintenanceService
	ArchiveCredentials backupcredentials.ArchiveService
	DataInitialization dispatch.DataInitializationOperations
	previewStore       *store.Store
	Reconciler         dispatch.ReconcileOperations
	Inventory          dispatch.Inventory
	Planner            dispatch.Planner
	Jobs               dispatch.JobOperations
	Runner             jobs.Runner
	NativeCapture      func(context.Context, string) (jobs.Accepted, error)
	NativeBackups      dispatch.NativeBackupOperations
	NativeBackupStatus dispatch.NativeBackupStatusOperations
	NativeBackupPolicy dispatch.NativeBackupPolicyOperations
	NativeRecovery     dispatch.NativeRecoveryOperations
	NativeMaterial     dispatch.NativeMaterialOperations
	NativeLiveRestore  dispatch.NativeLiveRestoreOperations
	NativeRetire       dispatch.NativeRetireOperations
	BackupCredentials  dispatch.BackupCredentialOperations
	Config             dispatch.ConfigurationOperations
	Secrets            dispatch.SecretOperations
	Apps               dispatch.AppOperations
	Logs               dispatch.LogReader
	Diagnose           dispatch.DiagnosticReader
	Authorize          dispatch.Authorization
	close              func() error
}

func (r *Runtime) Close() error { return r.close() }
