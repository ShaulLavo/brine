package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/target"
)

type ServerFactory struct {
	version            string
	authenticated      string
	open               func(context.Context, string) (*Runtime, error)
	previewOpen        func(context.Context, string) (*Runtime, error)
	inventory          func(context.Context) (dispatch.Inventory, error)
	closeRuntime       func() error
	diagnosticStateDir string
}

func NewServerFactory(version, capturedMarker string) *ServerFactory {
	return newServerFactory(version, capturedMarker, Open, func(ctx context.Context) (dispatch.Inventory, error) { return NewInventory(ctx) })
}
func newServerFactory(version, marker string, open func(context.Context, string) (*Runtime, error), inventory func(context.Context) (dispatch.Inventory, error)) *ServerFactory {
	return &ServerFactory{version: version, authenticated: marker, open: open, previewOpen: OpenPreview, inventory: inventory, diagnosticStateDir: "/home/brine/.local/state/brine"}
}
func (f *ServerFactory) Build(ctx context.Context, op string) (*dispatch.Server, error) {
	if _, ok := dispatch.ClassOf(op); !ok {
		return nil, result.New(result.DispatchOperationRefused, nil)
	}
	if op == "ping" {
		return nil, nil
	}
	if f.authenticated != "deploy" {
		return nil, result.New(result.DispatchOperationRefused, nil)
	}
	if op == "restore_test" {
		server := dispatch.NewServer(f.version, nil)
		server.RestoreTests = newRestoreTests(f.diagnosticStateDir)
		return server, nil
	}
	if op == "inventory" || op == "diagnose" {
		collector, err := f.inventory(ctx)
		if err != nil {
			return nil, err
		}
		server := dispatch.NewServer(f.version, collector)
		if op == "diagnose" {
			server.Diagnose, server.Logs = diagnosticReaders(f.diagnosticStateDir, collector)
		}
		return server, nil
	}
	open := f.open
	if op == "reconcile" && dispatch.IsReconcilePreview(ctx) {
		open = f.previewOpen
	}
	runtime, err := open(ctx, f.authenticated)
	if err != nil {
		return nil, err
	}
	f.closeRuntime = runtime.Close
	server := dispatch.NewServer(f.version, runtime.Inventory).WithJobs(runtime.Jobs, runtime.Authorize)
	server.Reconciler = runtime.Reconciler
	server.Planner = runtime.Planner
	server.Apps = runtime.Apps
	server.Config = runtime.Config
	server.Secrets = runtime.Secrets
	server.BackupCredentials = runtime.BackupCredentials
	server.Logs = runtime.Logs
	server.Diagnose = runtime.Diagnose
	return server, nil
}
func (f *ServerFactory) Close() error {
	if f.closeRuntime != nil {
		return f.closeRuntime()
	}
	return nil
}

// Diagnosis has no mutation runtime or preview fencing. DiskStore opens each
// bounded read with OpenReadOnly so unavailable state remains a partial report.
func diagnosticReaders(dir string, collector dispatch.Inventory) (dispatch.DiagnosticReader, dispatch.LogReader) {
	if hostCollector, ok := collector.(*inventory.Collector); ok {
		readCollector := *hostCollector
		readCollector.StateInventory = func(ctx context.Context) (target.ControlInventory, error) {
			state, err := store.OpenReadOnly(ctx, dir)
			if err != nil {
				return target.ControlInventory{}, err
			}
			defer state.Close()
			return state.InventoryState(ctx)
		}
		collector = &readCollector
	}
	logReader := logs.Reader{Inventory: collector, Executor: localexec.ExecRunner{}}
	reader := diagnose.Reader{Inventory: collector, Store: diagnose.DiskStore{Dir: dir}, Logs: logReader, Runner: localexec.ExecRunner{}, FS: inventory.HostFS{}, MinimumFreeDiskBytes: diagnosticMinimumFreeDiskBytes}
	return reader, logReader
}
