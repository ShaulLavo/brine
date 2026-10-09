package host

import (
	"context"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/result"
)

type ServerFactory struct {
	version       string
	authenticated string
	open          func(context.Context, string) (*Runtime, error)
	inventory     func(context.Context) (dispatch.Inventory, error)
	closeRuntime  func() error
}

func NewServerFactory(version, capturedMarker string) *ServerFactory {
	return newServerFactory(version, capturedMarker, Open, func(ctx context.Context) (dispatch.Inventory, error) { return NewInventory(ctx) })
}
func newServerFactory(version, marker string, open func(context.Context, string) (*Runtime, error), inventory func(context.Context) (dispatch.Inventory, error)) *ServerFactory {
	return &ServerFactory{version: version, authenticated: marker, open: open, inventory: inventory}
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
	if op == "inventory" {
		collector, err := f.inventory(ctx)
		if err != nil {
			return nil, err
		}
		return dispatch.NewServer(f.version, collector), nil
	}
	runtime, err := f.open(ctx, f.authenticated)
	if err != nil {
		return nil, err
	}
	f.closeRuntime = runtime.Close
	server := dispatch.NewServer(f.version, runtime.Inventory).WithJobs(runtime.Jobs, runtime.Authorize)
	server.Reconciler = runtime.Reconciler
	server.Planner = runtime.Planner
	server.Apps = runtime.Apps
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
