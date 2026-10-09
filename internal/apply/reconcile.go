package apply

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

type resolution uint8

const (
	unresolved resolution = iota
	applied
	notApplied
)

type writerState uint8

const (
	writerUnknown writerState = iota
	writerStopped
	writerRunning
	writerPending
)

// Inspection is read-only and happens only after the unknown outcome is durable.
// A running service is not evidence of an artifact, reload or commit outcome.
func (x *execution) reconcileUnknown(ctx context.Context, step string) resolution {
	budget := x.executor.effectTimeout()
	if step == "start_unit" {
		budget += x.executor.effectTimeout() + time.Duration(x.desired.Health.StartupDeadlineSeconds)*time.Second
	}
	if step == "rollback_start" {
		budget += x.executor.effectTimeout() + time.Duration(x.previousDesired.Health.StartupDeadlineSeconds)*time.Second
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	probeCtx, probeCancel := context.WithTimeout(ctx, x.executor.effectTimeout())
	writer := x.waitWriter(probeCtx)
	probeCancel()
	var result resolution
	switch step {
	case "withdraw_route", "remove_unit":
		if x.executor.Facts != nil {
			_, _ = x.executor.Facts.Read(ctx)
		}
	case "retire_app":
		if store, ok := x.executor.Releases.(RetirementStore); ok && x.plan.Removal != nil {
			retired, err := store.AppRetired(ctx, x.id, x.plan.App, x.plan.Removal.ReleaseID)
			if err == nil && retired {
				result = applied
			}
		}
	case "start_unit", "rollback_start":
		if writer == writerStopped {
			result = notApplied
			break
		}
		if writer != writerRunning {
			break
		}
		desired, port := x.desired, x.plan.HostPort
		if step == "rollback_start" {
			desired, port = x.previousDesired, x.previous.HostPort
		}
		if x.executor.Health != nil && x.check(ctx, desired, port, false) == nil {
			// Health cannot fence a manager job queued while the probe was in flight.
			settledCtx, settledCancel := context.WithTimeout(ctx, x.executor.effectTimeout())
			settled := x.waitWriter(settledCtx)
			settledCancel()
			if settled == writerRunning {
				result = applied
			} else if settled == writerStopped {
				result = notApplied
			}
		}
	case "quiesce_old", "rollback_quiesce", "stop_unit":
		if writer == writerStopped {
			result = applied
		} else if writer == writerRunning {
			result = notApplied
		}
	case "pull_image":
		image, err := podman.ParseImage(string(x.desired.Image))
		if err != nil || x.executor.Podman == nil {
			break
		}
		info, err := x.executor.Podman.Inspect(ctx, image)
		if isNotFound(err) {
			result = notApplied
			break
		}
		if err != nil {
			break
		}
		if x.plan.Image.ManifestDigest.Value != nil && info.IndexDigest == x.plan.Image.Digest && info.ManifestDigest == *x.plan.Image.ManifestDigest.Value && info.Platform.OS == x.plan.Image.Platform.OS && info.Platform.Architecture == x.plan.Image.Platform.Arch {
			result = applied
		} else {
			result = notApplied
		}
	case "install_unit", "rollback_unit":
		if writer != writerStopped || x.executor.Facts == nil {
			break
		}
		facts, err := x.executor.Facts.Read(ctx)
		if err != nil {
			break
		}
		hash, known := observedUnitHash(facts, x.plan.App, x.unit.Name())
		if !known {
			break
		}
		wanted, before := x.unit.Hash(), x.previousUnitHash()
		if step == "rollback_unit" {
			wanted, before = before, wanted
		}
		if hash == wanted {
			result = applied
		} else if hash == before {
			result = notApplied
		}
	case "commit":
		if x.nextRelease == nil || x.executor.Releases == nil {
			break
		}
		current, exists, err := x.executor.Releases.CurrentRelease(ctx, x.plan.App)
		if err != nil {
			break
		}
		if exists && reflect.DeepEqual(current, *x.nextRelease) {
			result = applied
		} else if exists == x.hasPrevious && (!exists || reflect.DeepEqual(current, x.previous)) {
			result = notApplied
		}
		// Staging and systemd/Caddy reloads have no authoritative read-back in these
		// adapter contracts. Unit/container health cannot prove those effects. Leave
		// them unknown; P03-07 must inspect the relevant staged/loaded generation.
	}
	if ctx.Err() != nil {
		return unresolved
	}
	return result
}

func (x *execution) inspectWriter(ctx context.Context) writerState {
	if x.executor.Systemd == nil || x.executor.Podman == nil {
		return writerUnknown
	}
	unit, err := systemd.ParseUnit(x.plan.App + ".service")
	if err != nil {
		return writerUnknown
	}
	name, err := podman.ParseName("systemd-" + x.plan.App)
	if err != nil {
		return writerUnknown
	}
	pendingBefore, jobErr := x.executor.Systemd.JobPending(ctx, unit)
	properties, unitErr := x.executor.Systemd.Show(ctx, unit)
	container, containerErr := x.executor.Podman.ContainerState(ctx, name)
	pendingAfter, afterJobErr := x.executor.Systemd.JobPending(ctx, unit)
	if ctx.Err() != nil {
		return writerUnknown
	}
	if (jobErr == nil && pendingBefore) || (afterJobErr == nil && pendingAfter) {
		return writerPending
	}
	if unitErr == nil {
		switch properties.ActiveState {
		case "activating", "deactivating", "reloading", "refreshing":
			return writerPending
		}
		switch properties.SubState {
		case "start", "start-pre", "start-post", "stop", "stop-sigterm", "stop-sigkill", "stop-post", "auto-restart", "reload":
			return writerPending
		}
	}
	if jobErr != nil || afterJobErr != nil {
		return writerUnknown
	}
	unitStopped := isNotFound(unitErr) || (unitErr == nil && ((properties.ActiveState == "inactive" && properties.SubState == "dead") || (properties.ActiveState == "failed" && properties.SubState == "failed")))
	containerStopped := isNotFound(containerErr) || (containerErr == nil && !container.Running && (container.Status == "exited" || container.Status == "stopped" || container.Status == "created"))
	if unitStopped && containerStopped {
		return writerStopped
	}
	if unitErr == nil && properties.ActiveState == "active" && properties.SubState == "running" && containerErr == nil && container.Running && container.Status == "running" {
		return writerRunning
	}
	return writerUnknown
}
func isNotFound(err error) bool {
	var runtime *localexec.Error
	return errors.As(err, &runtime) && runtime.Kind == localexec.NotFound
}
func observedUnitHash(facts Facts, app, name string) (string, bool) {
	apps := facts.Input.Snapshot.Apps
	if apps.Status != target.KnownStatus || apps.Value == nil {
		return "", false
	}
	for _, installed := range *apps.Value {
		if installed.Name != app {
			continue
		}
		units := installed.QuadletUnits
		if units.Status != target.KnownStatus || units.Value == nil {
			return "", false
		}
		for _, unit := range *units.Value {
			if unit.Name == name {
				return unit.Hash, true
			}
		}
		return "", true
	}
	return "", true
}

// Poll only manager jobs or explicit unit transitions, under the caller's
// bounded probe deadline. An unreadable queue is not a settled empty queue.
func (x *execution) waitWriter(ctx context.Context) writerState {
	for {
		writer := x.inspectWriter(ctx)
		if writer != writerPending {
			return writer
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return writerUnknown
		case <-timer.C:
		}
	}
}
