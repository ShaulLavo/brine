//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/apply"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/store"
	"path/filepath"
	"time"
)

func (h archiveHost) nativeArchiveStopped(ctx context.Context, p archive.Plan) (bool, error) {
	d, ok := p.Candidate.Databases[0].Native()
	if !ok {
		return false, archive.ErrRefused
	}
	stopped, err := h.nativeArchiveServiceStopped(ctx, engineContainerName(d.Binding.BindingID)+".service")
	if err != nil || !stopped {
		return false, archive.ErrRefused
	}
	containers, err := listEngineContainers(ctx, h.session)
	if err != nil || !engineSourceAbsent(containers, d.Binding.SourcePath()) || !nativeRestoreContainersAbsent(containers, d.Binding.BindingID, p.Candidate.App) {
		return false, archive.ErrRefused
	}
	if d.State == data.EngineUnpublished {
		stopped, err = h.nativeArchiveServiceStopped(ctx, p.Candidate.App+".service")
		if err != nil || !stopped {
			return false, archive.ErrRefused
		}
	}
	return true, ctx.Err()
}

func (h archiveHost) nativeArchiveServiceStopped(ctx context.Context, name string) (bool, error) {
	stopped, err := h.serviceStopped(ctx, name, false)
	if err != nil || stopped {
		return stopped, err
	}
	return h.serviceStopped(ctx, name, true)
}

func (h archiveHost) nativeArchiveLifetimeLocks(ctx context.Context, p archive.Plan) (func() error, error) {
	d, ok := p.Candidate.Databases[0].Native()
	if !ok {
		return nil, archive.ErrRefused
	}
	lock, err := data.AcquireEngineLifetimeLock(ctx, h.stateRoot, d.Binding.BindingID)
	if err != nil {
		return nil, err
	}
	if h.nativeLifetimes == nil {
		_ = lock.Release()
		return nil, archive.ErrRefused
	}
	h.nativeLifetimes[d.Binding.BindingID] = lock
	return func() error { delete(h.nativeLifetimes, d.Binding.BindingID); return lock.Release() }, nil
}

func (h archiveHost) coverNativeArchive(ctx context.Context, p archive.Plan, d archive.NativeDatabase, operation string, writersAt time.Time) (archive.Coverage, error) {
	var zero archive.Coverage
	pointID := ""
	for _, ref := range d.Dependencies {
		if ref.Kind == data.EnginePreparedPointEvidence && ref.EpochID == d.Head.EpochID {
			pointID = ref.ID
		}
	}
	point, err := h.service.Store.ReadNativePreparedPoint(ctx, pointID)
	if err != nil || point.RuntimeSHA256 != d.Head.RuntimeSHA256 || point.FenceOwnerOpID == "" || !time.Now().UTC().Before(point.ExpiresAt) {
		return zero, archive.ErrRefused
	}
	preparation, err := h.service.Store.AcquireEnginePreparation(ctx, d.Binding.BindingID, point.FenceID, point.FenceOwnerOpID)
	if err != nil {
		return zero, err
	}
	defer func() { _ = preparation.Close() }()
	lifetime, err := data.AcquireEngineLifetimeLock(ctx, h.stateRoot, d.Binding.BindingID)
	if err != nil {
		return zero, err
	}
	defer func() { _ = lifetime.Release() }()
	r := nativeBackupRuntime{state: h.service.Store, stateRoot: h.stateRoot, home: h.home, session: h.session}
	original, err := h.service.Store.ReadNativePreparedStop(ctx, pointID)
	if err != nil || original.Mode != "clean_exit" && (original.Mode != "unpublished_absent" || d.State != data.EngineUnpublished) {
		return zero, archive.ErrRefused
	}
	if stopped, err := h.nativeArchiveStopped(ctx, p); err != nil || !stopped {
		return zero, archive.ErrRefused
	}
	current := original
	if original.Mode == "clean_exit" {
		inspected, err := h.session.Execute(ctx, "/usr/bin/podman", []string{"container", "inspect", original.ContainerID}, nil, false)
		if err != nil {
			return zero, archive.ErrRefused
		}
		finished, err := decodeNativeStoppedState([]byte(inspected.Stdout), original.ContainerID, time.Time{}, time.Now().UTC())
		if err != nil || !finished.Equal(original.FinishedAt) {
			return zero, archive.ErrRefused
		}
	} else {
		current.FinishedAt = time.Now().UTC()
	}
	// The owned retirement claim retains the original preparation operation.
	stopped, err := h.service.Store.ResumeNativePreparedPoint(ctx, preparation, lifetime, pointID, current)
	if err != nil {
		return zero, err
	}
	stage := filepath.Join(h.stateRoot, "native-archive-coverage")
	if err = ensurePrivateChild(h.stateRoot, "native-archive-coverage"); err != nil {
		return zero, err
	}
	nonce, err := data.NewID()
	if err != nil {
		return zero, err
	}
	copied, err := copyEngineSource(ctx, d.Binding, stage, nonce, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	if err != nil {
		return zero, err
	}
	defer func() { _ = copied.discard() }()
	if d.State == data.EngineUnpublished {
		return h.coverNativeUnpublishedArchive(ctx, p, stopped, copied, r, writersAt)
	}
	source, err := reopenNativeCheckpoint(ctx, h.service.Store, stopped, copied)
	if err != nil {
		return zero, err
	}
	restored, err := r.restorePreparedSource(ctx, stopped, source)
	if err != nil {
		return zero, err
	}
	defer func() { _ = restored.close() }()
	if restored.unchanged(ctx) != nil {
		return zero, archive.ErrRefused
	}
	if err = h.verifyNativeArchiveClosedRoute(ctx, p); err != nil {
		return zero, err
	}

	bundle, err := h.service.Store.ReadNativePreparedBundle(ctx, stopped)
	_, selection, initialized := bundle.Source().Initialized()
	if err != nil || !initialized {
		return zero, archive.ErrRefused
	}
	evidence, disposal, err := r.verifyExtracted(ctx, restored.extracted, restored.parent, filepath.Dir(restored.path), source.checkpoint, selection, bundle.SourceEvidence(), bundle.Schema(), bundle.ConfirmedBound(), bundle.FinalPairs())
	if err != nil || restored.unchanged(ctx) != nil {
		return zero, archive.ErrRefused
	}
	return h.service.Store.RecordNativeArchiveVerified(ctx, p.ArchiveID, stopped, restored.extracted, store.NativeArchiveVerificationObservation{Parent: restored.parent, Evidence: evidence, ReaderNonce: disposal.nonce, ReaderViewSHA256: disposal.viewHash, ReaderImageID: disposal.imageID, ReaderDisposedAt: disposal.at, WritersQuiescedAt: writersAt})
}

func (h archiveHost) coverNativeUnpublishedArchive(ctx context.Context, p archive.Plan, stopped *store.NativeStoppedPreparation, copied *engineSourceCopy, runtime nativeBackupRuntime, writersAt time.Time) (archive.Coverage, error) {
	empty, err := reopenNativeEmptySource(ctx, h.service.Store, h.stateRoot, stopped, copied)
	if err != nil {
		return archive.Coverage{}, err
	}
	derived, err := runtime.restorePreparedEmpty(ctx, empty)
	if err != nil {
		return archive.Coverage{}, err
	}
	defer func() { _ = derived.close() }()
	if derived.unchanged(ctx) != nil || h.verifyNativeArchiveClosedRoute(ctx, p) != nil {
		return archive.Coverage{}, archive.ErrRefused
	}
	return h.service.Store.RecordNativeArchiveVerified(ctx, p.ArchiveID, stopped, derived.extracted, store.NativeArchiveVerificationObservation{Parent: derived.parent, WritersQuiescedAt: writersAt})
}

func (h archiveHost) verifyNativeArchiveClosedRoute(ctx context.Context, p archive.Plan) error {
	if p.Candidate.ReleaseID == "" {
		return h.verifyNativeUnpublishedArchiveRoute(ctx, p)
	}
	route, err := h.service.Store.ReadManagedRouteState(ctx, p.Candidate.App)
	routes, ok := h.routes.(apply.GenerationRoutes)
	if err != nil || route.Present || !ok || routes.Manager == nil {
		return archive.ErrRefused
	}
	loaded, err := routes.Manager.Observe()
	if err != nil || loaded.Generation != route.Generation || loaded.Files[p.Candidate.App+".caddy"] != "" || routes.Manager.CheckDrift(loaded) != nil {
		return archive.ErrRefused
	}
	release, err := h.service.Store.CurrentRelease(ctx, p.Candidate.App)
	if err != nil || release.ID != p.Candidate.ReleaseID {
		return archive.ErrRefused
	}
	planned, desired, err := h.service.Store.LoadPlan(ctx, release.PlanID)
	if err != nil || planned.Managed == nil {
		return archive.ErrRefused
	}
	site, err := caddy.CommittedManagedSite(desired, *planned.Managed)
	if err != nil || routes.Manager.InspectLoadedChange(ctx, loaded, caddy.State{Sites: map[string]caddy.Site{p.Candidate.App + ".caddy": site}}) != nil {
		return archive.ErrRefused
	}
	return ctx.Err()
}

func (h archiveHost) verifyNativeUnpublishedArchiveRoute(ctx context.Context, p archive.Plan) error {
	d, _ := p.Candidate.Databases[0].Native()
	if d.State != data.EngineUnpublished {
		return archive.ErrRefused
	}
	route, err := h.service.Store.ReadManagedRouteState(ctx, p.Candidate.App)
	if err != nil && !errors.Is(err, store.ErrNotFound) || err == nil && (route.Present || route.BindingID != d.Binding.BindingID) {
		return archive.ErrRefused
	}
	routes, ok := h.routes.(apply.GenerationRoutes)
	if !ok || routes.Manager == nil {
		return archive.ErrRefused
	}
	loaded, err := routes.Manager.Observe()
	if err != nil || loaded.Files[p.Candidate.App+".caddy"] != "" || routes.Manager.CheckDrift(loaded) != nil {
		return archive.ErrRefused
	}
	preparation, err := h.service.Store.ReadManagedPreparation(ctx, d.Binding.BindingID)
	if err != nil {
		return err
	}
	planned, desired, err := h.service.Store.LoadPlan(ctx, preparation.PlanID)
	if err != nil || planned.Managed == nil || planned.Managed.Database != d.Binding || preparation.EnginePort < 1024 {
		return archive.ErrRefused
	}
	// This read-only site describes domains that must be absent, not a managed
	// initialized service or permission to publish a route.
	site, err := caddy.CommittedSite(desired, spec.Port(preparation.EnginePort))
	if err != nil || routes.Manager.InspectLoadedChange(ctx, loaded, caddy.State{Sites: map[string]caddy.Site{p.Candidate.App + ".caddy": site}}) != nil {
		return archive.ErrRefused
	}
	return ctx.Err()
}
