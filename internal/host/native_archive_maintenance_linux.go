//go:build linux

package host

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
)

func (h archiveHost) nativeArchiveMaintenanceAdmission(ctx context.Context, p archive.Plan, m archive.Manifest, d archive.NativeDatabase, pol policy.Policy) (bool, error) {
	if d.Allocation == nil || !slices.Contains(pol.PersistentRoots(), string(d.Binding.Root)) {
		return false, archive.ErrRefused
	}
	root, err := data.InspectRoot(string(d.Binding.Root))
	if err == nil {
		root, err = root.WithPreparationProof(d.Allocation.RootProof)
	}
	if err != nil || !root.Admits(d.Binding.Root, 0) {
		return false, archive.ErrRefused
	}
	var coverage archive.NativeCoverage
	for _, c := range m.Coverage {
		native, ok := c.Native()
		if ok && native.DatabaseID == d.Binding.DatabaseID {
			coverage = native
		}
	}
	point, err := h.service.Store.ReadNativePreparedPoint(ctx, coverage.Point.ID)
	if err != nil || point.BindingID != d.Binding.BindingID || point.FenceID != coverage.FenceID || point.FenceOwnerOpID != coverage.FenceOwnerOpID {
		return false, archive.ErrRefused
	}
	fence, err := h.service.Store.ReadNativeArchiveFence(ctx, p.ArchiveID, d.Binding.BindingID)
	if err != nil || fence.ID != point.FenceID || fence.OperationID != point.FenceOwnerOpID || fence.DatabaseID != d.Binding.DatabaseID || fence.IncarnationID != p.Candidate.Incarnation || fence.State != data.FenceHeld {
		return false, archive.ErrRefused
	}
	admitted := true
	for _, epoch := range d.Epochs {
		destination, ok := pol.BackupDestination(epoch.Destination.Reference)
		if !ok || destination != epoch.Destination {
			return false, archive.ErrRefused
		}
		retention, known := pol.BackupRetention(epoch.Destination.Reference)
		admitted = admitted && known && retention.Admits(epoch.Destination, time.Now().UTC())
	}
	return admitted, nil
}

func (h archiveHost) nativeArchiveMaintenanceQuiescent(ctx context.Context, p archive.Plan, d archive.NativeDatabase) error {
	stopped, err := h.nativeArchiveServiceStopped(ctx, engineContainerName(d.Binding.BindingID)+".service")
	if err != nil || !stopped {
		return archive.ErrRefused
	}
	containers, err := listEngineContainers(ctx, h.session)
	if err != nil {
		return archive.ErrRefused
	}
	for _, container := range containers {
		owned := container.Name == engineContainerName(d.Binding.BindingID) || container.Config.Labels["BRINE_ENGINE_BINDING"] == string(d.Binding.BindingID)
		if owned && (*container.State.Running || *container.State.PID != 0) {
			return archive.ErrRefused
		}
	}
	for _, tree := range p.Trees {
		if !engineSourceAbsent(containers, filepath.Join(string(tree.Root), tree.SourceRelative)) || !engineSourceAbsent(containers, filepath.Join(string(tree.Root), tree.ArchiveRelative)) {
			return archive.ErrRefused
		}
	}
	for _, tree := range d.TransitionTrees {
		original := filepath.Join(string(d.Binding.Root), "transitions", tree.TransitionID)
		retained := filepath.Join(string(d.Binding.Root), "archives", string(p.ArchiveID), "native-transitions", tree.TransitionID)
		if !engineSourceAbsent(containers, original) || !engineSourceAbsent(containers, retained) {
			return archive.ErrRefused
		}
	}
	return ctx.Err()
}

func nativeArchiveRetentionDeadline(pol policy.Policy, d archive.NativeDatabase, deadline time.Time) (time.Time, error) {
	for _, epoch := range d.Epochs {
		retention, known := pol.BackupRetention(epoch.Destination.Reference)
		if !known || !retention.Admits(epoch.Destination, time.Now().UTC()) {
			return time.Time{}, archive.ErrRefused
		}
		verified, err := time.Parse(time.RFC3339Nano, retention.VerifiedAt)
		if err != nil {
			return time.Time{}, archive.ErrRefused
		}
		// Admits bounds freshness to thirty days before this conversion.
		until := verified.Add(time.Duration(retention.FreshnessSeconds) * time.Second) //nolint:gosec // Bounded by retention admission above.
		if until.Before(deadline) {
			deadline = until
		}
	}
	return deadline, nil
}
