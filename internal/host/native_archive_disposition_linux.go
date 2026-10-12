//go:build linux

package host

import (
	"context"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/ops"
	"golang.org/x/sys/unix"
	"path/filepath"
	"reflect"
)

func (h archiveHost) recheckNativeArchive(ctx context.Context, p archive.Plan, o archive.Operation) error {
	fresh, err := h.archiveFacts(ctx, p.Candidate.App, o.ID, true)
	if err != nil || fresh.PolicyHash != p.PolicyHash || fresh.TargetHash != p.TargetHash || fresh.Generation != p.Generation || !reflect.DeepEqual(fresh.Candidate, p.Candidate) {
		return archive.ErrRefused
	}
	if stopped, err := h.nativeArchiveStopped(ctx, p); err != nil || !stopped {
		return archive.ErrRefused
	}
	d, _ := p.Candidate.Databases[0].Native()
	if err = h.verifyNativeArchiveDataSeal(ctx, p, d); err != nil {
		return err
	}
	return h.verifyNativeArchiveClosedRoute(ctx, p)
}

func (h archiveHost) retireNativeArchiveRuntime(ctx context.Context, p archive.Plan) error {
	if err := h.verifyNativeArchiveUnits(ctx, p.Candidate); err != nil {
		return err
	}
	if err := h.verifyNativeArchiveClosedRoute(ctx, p); err != nil {
		return err
	}
	d, _ := p.Candidate.Databases[0].Native()
	pointID := ""
	for _, ref := range d.Dependencies {
		if ref.Kind == data.EnginePreparedPointEvidence {
			pointID = ref.ID
		}
	}
	point, err := h.service.Store.ReadNativePreparedPoint(ctx, pointID)
	if err != nil {
		return err
	}
	preparation, err := h.service.Store.AcquireEnginePreparation(ctx, d.Binding.BindingID, point.FenceID, point.FenceOwnerOpID)
	if err != nil {
		return err
	}
	defer func() { _ = preparation.Close() }()
	release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
	if err != nil || release.ID != p.Candidate.ReleaseID || release.Managed == nil {
		return archive.ErrRefused
	}
	if release.Managed.Frontend != nil {
		unit := release.Managed.Frontend.Unit
		if h.units == nil || h.units.RemoveArchived(ctx, unit.Name, unit.Hash) != nil {
			return archive.ErrRecovery
		}
	}
	if err = h.retainNativeArchiveUnit(ctx, p, release); err != nil {
		return err
	}
	result, err := h.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "daemon-reload"}, nil, true)
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return archive.ErrRecovery
	}
	stopped, err := h.nativeArchiveRuntimeAbsent(ctx, p)
	if err != nil || !stopped {
		return archive.ErrRecovery
	}
	return nil
}

func (h archiveHost) retainNativeArchiveUnit(ctx context.Context, p archive.Plan, release ops.Release) error {
	units, err := openManagedUnits(h.home)
	if err != nil {
		return err
	}
	defer func() { _ = units.Close() }()
	if err = units.directory(managedServiceStaging, true); err != nil {
		return err
	}
	name := release.Managed.EngineUnit.Name
	from := filepath.Join(managedServiceDirectory, name)
	to := filepath.Join(managedServiceStaging, "retired-"+string(p.ArchiveID)+"-"+name)
	raw, present, err := units.read(ctx, from)
	if err != nil || !present || "sha256:"+managedServiceHash(raw) != release.Managed.EngineUnit.Hash {
		return archive.ErrRefused
	}
	src, err := engineOpenMember(units.root, managedServiceDirectory, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dest, err := engineOpenMember(units.root, managedServiceStaging, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = dest.Close() }()
	if err = unix.Renameat2(int(src.Fd()), name, int(dest.Fd()), filepath.Base(to), unix.RENAME_NOREPLACE); err != nil {
		return archive.ErrRecovery
	}
	moved, present, err := units.read(ctx, to)
	if err != nil || !present || "sha256:"+managedServiceHash(moved) != release.Managed.EngineUnit.Hash || src.Sync() != nil || dest.Sync() != nil {
		return archive.ErrRecovery
	}
	return ctx.Err()
}

func (h archiveHost) nativeArchiveRuntimeAbsent(ctx context.Context, p archive.Plan) (bool, error) {
	d, _ := p.Candidate.Databases[0].Native()
	absent, err := h.serviceStopped(ctx, engineContainerName(d.Binding.BindingID)+".service", true)
	if err != nil || !absent {
		return false, archive.ErrRefused
	}
	release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
	if err != nil || release.ID != p.Candidate.ReleaseID || release.Managed == nil {
		return false, archive.ErrRefused
	}
	if release.Managed.Frontend != nil {
		absent, err = h.serviceStopped(ctx, p.Candidate.App+".service", true)
		if err != nil || !absent {
			return false, archive.ErrRefused
		}
	}
	containers, err := listEngineContainers(ctx, h.session)
	if err != nil || !nativeRestoreContainersAbsent(containers, d.Binding.BindingID, p.Candidate.App) {
		return false, archive.ErrRefused
	}
	if err = h.verifyNativeArchiveClosedRoute(ctx, p); err != nil {
		return false, err
	}
	return true, ctx.Err()
}
