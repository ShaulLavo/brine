//go:build linux

package host

import (
	"context"
	"errors"
	"strings"

	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/systemd"
)

func (w *nativePrepareWork) stopFrontend(ctx context.Context) error {
	release, err := w.runtime.state.CurrentRelease(ctx, w.operation.App)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || release.Managed == nil {
		return errEngineSource
	}
	if release.Managed.Frontend == nil {
		return nil
	}
	frontend := release.Managed.Frontend
	if quadlet.VerifyCurrent(ctx, w.runtime.home, frontend.Unit.Name, frontend.Unit.Hash) != nil {
		return errEngineSource
	}
	name, valid := strings.CutSuffix(frontend.Unit.Name, ".container")
	if !valid || name != w.operation.App {
		return errEngineSource
	}
	unit, err := systemd.ParseUnit(name + ".service")
	if err != nil {
		return err
	}
	pending, err := systemd.New(w.runtime.session).JobPending(ctx, unit)
	if err != nil || pending {
		return errEngineSource
	}
	if err := w.runtime.state.RecordNativePreparedFrontendStop(ctx, w.native, false); err != nil {
		return err
	}
	if err := systemd.New(w.runtime.session).Stop(ctx, unit); err != nil {
		return err
	}
	if quadlet.VerifyCurrent(ctx, w.runtime.home, frontend.Unit.Name, frontend.Unit.Hash) != nil {
		return errEngineSource
	}
	host := archiveHost{session: w.runtime.session}
	stopped, err := host.serviceStopped(ctx, unit.String(), false)
	if err != nil || !stopped {
		return errEngineSource
	}
	container, err := managedFrontendContainerName(w.operation.App)
	if err != nil {
		return err
	}
	state, err := podman.New(w.runtime.session).ContainerState(ctx, container)
	if !managedFrontendStopped(state, err) {
		return errEngineSource
	}
	return w.runtime.state.RecordNativePreparedFrontendStop(ctx, w.native, true)
}
