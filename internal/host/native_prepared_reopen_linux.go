//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativePreparedReopen struct {
	preparation *store.EnginePreparation
	lifetime    *data.EngineLock
	stopped     *store.NativeStoppedPreparation
	copy        *engineSourceCopy
	restored    *nativeRestoredSource
}

func (r nativeBackupRuntime) reopenPreparedSource(ctx context.Context, pointID string, routes *caddy.Manager, withdrawn, previous caddy.State) (_ *nativePreparedReopen, err error) {
	point, expected, app, err := r.state.ReadNativePreparedStopExpectation(ctx, pointID)
	if err != nil || !data.ValidID(pointID) || app == "" || routes == nil {
		return nil, errEngineSource
	}
	preparation, err := r.state.AcquireEnginePreparation(ctx, point.BindingID, point.FenceID, point.PreparationOperationID)
	if err != nil {
		return nil, err
	}
	held := &nativePreparedReopen{preparation: preparation}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, held.close())
		}
	}()
	held.lifetime, err = data.AcquireEngineLifetimeLock(ctx, r.stateRoot, point.BindingID)
	if err != nil {
		return nil, err
	}
	permit, err := r.state.ReadEnginePermit(ctx, point.BindingID)
	if err != nil {
		return nil, err
	}
	current, err := r.inspectPreparedStop(ctx, app, permit, expected, routes, withdrawn, previous)
	if err != nil {
		return nil, err
	}
	held.stopped, err = r.state.ResumeNativePreparedPoint(ctx, preparation, held.lifetime, pointID, current)
	if err != nil {
		return nil, err
	}
	name, err := data.NewID()
	if err != nil {
		return nil, err
	}
	held.copy, err = copyEngineSource(ctx, permit.Reserved.Database, filepath.Join(r.stateRoot, "staging"), name, engineCopyLimits{Bytes: permit.Runtime.CopyBytes, Files: permit.Runtime.CopyFiles, Depth: 16})
	if err != nil {
		return nil, err
	}
	if point.SourceKind == "unpublished" {
		empty, openErr := reopenNativeEmptySource(ctx, r.state, r.stateRoot, held.stopped, held.copy)
		if openErr != nil {
			return nil, openErr
		}
		held.restored, err = r.restorePreparedEmpty(ctx, empty)
	} else {
		original, openErr := reopenNativeCheckpoint(ctx, r.state, held.stopped, held.copy)
		if openErr != nil {
			return nil, openErr
		}
		held.restored, err = r.restorePreparedSource(ctx, held.stopped, original)
	}
	if err != nil || !preparation.Active(point.BindingID) || !held.lifetime.HoldsLifetime(r.stateRoot, point.BindingID) {
		return nil, errors.Join(errEngineSource, err)
	}
	complete = true
	return held, nil
}

func (r nativeBackupRuntime) inspectPreparedStop(ctx context.Context, app string, permit store.EnginePermit, expected store.NativeStopObservation, routes *caddy.Manager, withdrawn, previous caddy.State) (store.NativeStopObservation, error) {
	if _, present := withdrawn.Sites[app+".caddy"]; present {
		return store.NativeStopObservation{}, errEngineSource
	}
	if routes.InspectLoadedChange(ctx, withdrawn, previous) != nil {
		return store.NativeStopObservation{}, errEngineSource
	}
	host := archiveHost{session: r.session}
	for _, unit := range []string{engineContainerName(permit.Runtime.BindingID) + ".service", app + ".service"} {
		if err := inspectPreparedUnitStopped(ctx, host, unit); err != nil {
			return store.NativeStopObservation{}, err
		}
	}
	containers, err := listEngineContainers(ctx, r.session)
	if err != nil || !engineBindingAbsent(containers, permit) {
		return store.NativeStopObservation{}, errEngineSource
	}
	if expected.Mode == "unpublished_absent" {
		if !preparedEmptyContainerAbsent(containers, permit) {
			return store.NativeStopObservation{}, errEngineSource
		}
		expected.FinishedAt = time.Now().UTC()
		return expected, nil
	}
	for _, container := range containers {
		if container.ID != expected.ContainerID {
			continue
		}
		if !ownedLiveEngine(container, permit, engineMaterialPaths(r.stateRoot, permit.Runtime)) {
			return store.NativeStopObservation{}, errEngineSource
		}
		result, err := r.session.Execute(ctx, "/usr/bin/podman", []string{"container", "inspect", container.ID}, nil, false)
		if err != nil {
			return store.NativeStopObservation{}, errEngineSource
		}
		finished, err := decodeNativeStoppedState([]byte(result.Stdout), container.ID, expected.FinishedAt, time.Now().UTC())
		if err != nil || !finished.Equal(expected.FinishedAt) || !strings.HasSuffix(container.State.CgroupPath, "/libpod-"+container.ID+".scope") {
			return store.NativeStopObservation{}, errEngineSource
		}
		if err = preparedContainerCgroupAbsent(container.ID); err != nil {
			return store.NativeStopObservation{}, errEngineSource
		}
		return expected, nil
	}
	return store.NativeStopObservation{}, errEngineSource
}

func inspectPreparedUnitStopped(ctx context.Context, host archiveHost, unit string) error {
	stopped, err := host.serviceStopped(ctx, unit, false)
	if err == nil && stopped {
		return nil
	}
	absent, err := host.serviceStopped(ctx, unit, true)
	if err != nil || !absent {
		return errEngineSource
	}
	return nil
}

func preparedEmptyContainerAbsent(containers []engineContainer, permit store.EnginePermit) bool {
	for _, container := range containers {
		if container.Name == engineContainerName(permit.Runtime.BindingID) || container.Config.Labels["BRINE_ENGINE_BINDING"] == string(permit.Runtime.BindingID) {
			return false
		}
	}
	return true
}

func preparedContainerCgroupAbsent(id string) error {
	if !data.ValidCatalogHash(id) {
		return errEngineSource
	}
	uid := strconv.Itoa(os.Geteuid())
	scope := "user.slice/user-" + uid + ".slice/user@" + uid + ".service/user.slice/libpod-" + id + ".scope"
	root, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = root.Close() }()
	if _, err = root.Lstat(scope); !os.IsNotExist(err) {
		return errEngineSource
	}
	return nil
}

func (r *nativePreparedReopen) close() error {
	var err error
	if r.restored != nil {
		err = errors.Join(err, r.restored.close())
	}
	if r.copy != nil {
		err = errors.Join(err, r.copy.discard())
	}
	if r.lifetime != nil {
		err = errors.Join(err, r.lifetime.Release())
	}
	if r.preparation != nil {
		err = errors.Join(err, r.preparation.Close())
	}
	return err
}
