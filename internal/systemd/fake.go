package systemd

import (
	"context"

	"github.com/ShaulLavo/brine/internal/localexec"
)

// Fake returns a failure for every unconfigured operation.
type Fake struct {
	DaemonReloadFunc func(context.Context) error
	StartFunc        func(context.Context, Unit) error
	StopFunc         func(context.Context, Unit) error
	RestartFunc      func(context.Context, Unit) error
	IsActiveFunc     func(context.Context, Unit) (bool, error)
	ShowFunc         func(context.Context, Unit) (Properties, error)
	ReloadCaddyFunc  func(context.Context) error
}

func unconfigured() error { return &localexec.Error{Kind: localexec.Failed} }
func (f *Fake) DaemonReload(c context.Context) error {
	if f.DaemonReloadFunc != nil {
		return f.DaemonReloadFunc(c)
	}
	return unconfigured()
}
func (f *Fake) Start(c context.Context, u Unit) error {
	if f.StartFunc != nil {
		return f.StartFunc(c, u)
	}
	return unconfigured()
}
func (f *Fake) Stop(c context.Context, u Unit) error {
	if f.StopFunc != nil {
		return f.StopFunc(c, u)
	}
	return unconfigured()
}
func (f *Fake) Restart(c context.Context, u Unit) error {
	if f.RestartFunc != nil {
		return f.RestartFunc(c, u)
	}
	return unconfigured()
}
func (f *Fake) IsActive(c context.Context, u Unit) (bool, error) {
	if f.IsActiveFunc != nil {
		return f.IsActiveFunc(c, u)
	}
	return false, unconfigured()
}
func (f *Fake) Show(c context.Context, u Unit) (Properties, error) {
	if f.ShowFunc != nil {
		return f.ShowFunc(c, u)
	}
	return Properties{}, unconfigured()
}
func (f *Fake) ReloadCaddy(c context.Context) error {
	if f.ReloadCaddyFunc != nil {
		return f.ReloadCaddyFunc(c)
	}
	return unconfigured()
}

var _ Adapter = (*Fake)(nil)
