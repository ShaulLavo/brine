//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

func (p DataPreparation) prepareAllocation(ctx context.Context, planned plan.Plan, desired policy.Desired) error {
	if p.State == nil || p.Runner == nil || desired.Runtime == nil || planned.MappingImage != data.MappingProbeImage || len(planned.DataAllocations) != len(desired.Databases) || len(planned.DataAllocations) == 0 {
		return data.ErrInvalid
	}
	for i, proposal := range planned.DataAllocations {
		declaration := desired.Databases[i]
		var destination data.Destination
		for _, candidate := range desired.BackupDestinations {
			if candidate.Reference == declaration.BackupDestination {
				destination = candidate
			}
		}
		if !proposal.Valid(declaration, destination) {
			return data.ErrInvalid
		}
	}
	scopes, scopeErr := p.State.ReadCredentialScopes(ctx, planned.App)
	if scopeErr != nil && !errors.Is(scopeErr, store.ErrNotFound) {
		return scopeErr
	}
	for _, scope := range scopes {
		found := false
		for _, proposal := range planned.DataAllocations {
			if scope.Database == proposal.Database && scope.Replica == proposal.Replica && !scope.FenceHeld {
				found = true
			}
		}
		if !found {
			return data.ErrInvalid
		}
	}
	// Provision the exact pinned prerequisite under approval, before any probe.
	pull, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err := p.Runner.Run(pull, "podman", "pull", data.MappingProbeImage)
	cancel()
	if err != nil {
		return err
	}
	probeRoot := p.ProbeRoot
	if probeRoot == nil {
		probeRoot = data.ProbeRoot
	}
	probeMapping := p.ProbeMapping
	if probeMapping == nil {
		probeMapping = inventory.ProbeDataMapping
	}
	roots := map[data.PersistentRoot]data.RootEvidence{}
	mappings := map[data.PersistentRoot]data.MappingEvidence{}
	for _, frozen := range planned.PreparationRoots {
		root, err := probeRoot(ctx, string(frozen.Root))
		if err != nil {
			return err
		}
		if root.Device != frozen.Device || root.Inode != frozen.Inode || root.Filesystem != frozen.Filesystem || !root.Admits(frozen.Root, desired.MinimumFreeDiskBytes) {
			return data.ErrInvalid
		}
		mapping, err := probeMapping(ctx, p.Runner, root, *desired.Runtime)
		if err != nil {
			return err
		}
		if mapping.Image != data.MappingProbeImage || !mapping.Admits(*desired.Runtime, root) {
			return data.ErrInvalid
		}
		roots[root.Root] = root
		mappings[root.Root] = mapping
	}
	for i, proposal := range planned.DataAllocations {
		declaration := desired.Databases[i]
		var destination data.Destination
		for _, candidate := range desired.BackupDestinations {
			if candidate.Reference == declaration.BackupDestination {
				destination = candidate
			}
		}
		if !proposal.Valid(declaration, destination) {
			return data.ErrInvalid
		}
		reserved, err := p.State.ReserveDatabase(ctx, store.DataReservation{App: planned.App, PolicyHash: desired.PolicyHash, Database: declaration, Destination: destination, Proposed: &proposal})
		if err != nil {
			return err
		}
		if reserved.Database != proposal.Database || reserved.Replica != proposal.Replica {
			return data.ErrInvalid
		}
		history, err := p.State.AllocationHistory(ctx, reserved.Database.DatabaseID)
		if err != nil {
			return err
		}
		if history {
			receipt, err := p.State.ReadAllocation(ctx, reserved.Database.DatabaseID)
			if err != nil {
				return err
			}
			if receipt == nil || data.ObserveSchemaWithAllocation(ctx, reserved.Database, nil, receipt).State != data.AllocatedEmpty {
				return data.ErrInvalid
			}
			continue
		}
		if _, err = data.PrepareDirectory(reserved.Database); err != nil {
			return err
		}
		receipt, err := data.CaptureAllocation(reserved.Database)
		if err != nil {
			return err
		}
		root, ok := roots[reserved.Database.Root]
		if !ok || receipt.RootDevice != root.Device || receipt.RootInode != root.Inode {
			return data.ErrInvalid
		}
		mapping := mappings[root.Root]
		receipt.RootProof = &root
		receipt.MappingProof = &mapping
		if err = p.State.RecordAllocation(ctx, receipt); err != nil {
			return err
		}
	}
	return p.State.RegisterSchemaDefinitions(ctx, planned.DataAllocations[0].Database.IncarnationID, desired.SchemaDefinitions)
}

func (p DataPreparation) allocationPrepared(ctx context.Context, planned plan.Plan, desired policy.Desired) (bool, error) {
	if p.State == nil || desired.Runtime == nil || len(planned.DataAllocations) == 0 || len(planned.DataAllocations) != len(desired.Databases) || planned.MappingImage != data.MappingProbeImage {
		return false, data.ErrInvalid
	}
	if p.Runner == nil {
		return false, data.ErrInvalid
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	raw, err := p.Runner.Run(probe, "podman", "image", "inspect", "--format={{json .RepoDigests}}", data.MappingProbeImage)
	cancel()
	if err != nil {
		return false, err
	}
	var digests []string
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &digests) != nil {
		return false, data.ErrInvalid
	}
	pinned := false
	for _, digest := range digests {
		if digest == data.MappingProbeImage {
			pinned = true
		}
	}
	if !pinned {
		return false, data.ErrInvalid
	}
	definitions, err := p.State.ReadSchemaDefinitions(ctx, planned.DataAllocations[0].Database.IncarnationID)
	if err != nil {
		return false, err
	}
	for _, expected := range desired.SchemaDefinitions {
		found := false
		for _, actual := range definitions {
			if actual == expected {
				found = true
			}
		}
		if !found {
			return false, data.ErrInvalid
		}
	}
	for i, proposal := range planned.DataAllocations {
		declaration := desired.Databases[i]
		var destination data.Destination
		for _, candidate := range desired.BackupDestinations {
			if candidate.Reference == declaration.BackupDestination {
				destination = candidate
			}
		}
		if !proposal.Valid(declaration, destination) {
			return false, data.ErrInvalid
		}
		reserved, err := p.State.ExistingDatabase(ctx, store.DataReservation{App: planned.App, Database: declaration, Destination: destination})
		if err != nil {
			return false, err
		}
		if reserved.Database != proposal.Database || reserved.Replica != proposal.Replica {
			return false, data.ErrInvalid
		}
		receipt, err := p.State.ReadAllocation(ctx, reserved.Database.DatabaseID)
		if err != nil {
			return false, err
		}
		if receipt == nil || receipt.RootProof == nil || receipt.MappingProof == nil {
			return false, data.ErrInvalid
		}
		root, err := data.InspectRoot(string(reserved.Database.Root))
		if err != nil {
			return false, err
		}
		proof := receipt.RootProof
		if root.Device != proof.Device || root.Inode != proof.Inode || root.Filesystem != proof.Filesystem || proof.Root != root.Root || !rootMatchesPreparation(root, planned.PreparationRoots) || !proof.Admits(root.Root, desired.MinimumFreeDiskBytes) || !receipt.MappingProof.Admits(*desired.Runtime, root) || receipt.MappingProof.Image != data.MappingProbeImage {
			return false, data.ErrInvalid
		}
		if data.ObserveSchemaWithAllocation(ctx, reserved.Database, definitions, receipt).State != data.AllocatedEmpty {
			return false, data.ErrInvalid
		}
	}
	return ctx.Err() == nil, ctx.Err()
}

func rootMatchesPreparation(root data.RootEvidence, planned []data.RootEvidence) bool {
	for _, frozen := range planned {
		if root.Root == frozen.Root && root.Device == frozen.Device && root.Inode == frozen.Inode && root.Filesystem == frozen.Filesystem {
			return true
		}
	}
	return false
}

// A deploy remeasures mapping only after approval. Inventory reuses immutable
// preparation evidence without creating probe files or invoking containers.
func (p DataPreparation) verifyApprovedMapping(ctx context.Context, planned plan.Plan, desired policy.Desired) error {
	if p.Runner == nil || desired.Runtime == nil {
		return data.ErrInvalid
	}
	probeRoot := p.ProbeRoot
	if probeRoot == nil {
		probeRoot = data.ProbeRoot
	}
	probeMapping := p.ProbeMapping
	if probeMapping == nil {
		probeMapping = inventory.ProbeDataMapping
	}
	seen := map[data.PersistentRoot]bool{}
	for _, mount := range planned.DataMounts {
		if seen[mount.Database.Root] {
			continue
		}
		seen[mount.Database.Root] = true
		receipt, err := p.State.ReadPreparationEvidence(ctx, mount.Database.DatabaseID)
		if err != nil {
			return err
		}
		if receipt == nil || receipt.RootProof == nil {
			return data.ErrInvalid
		}
		root, err := probeRoot(ctx, string(mount.Database.Root))
		if err != nil {
			return err
		}
		if !root.Admits(mount.Database.Root, desired.MinimumFreeDiskBytes) || root.Device != receipt.RootDevice || root.Inode != receipt.RootInode {
			return data.ErrInvalid
		}
		mapping, err := probeMapping(ctx, p.Runner, root, *desired.Runtime)
		if err != nil {
			return err
		}
		if mapping.Image != data.MappingProbeImage || !mapping.Admits(*desired.Runtime, root) {
			return data.ErrInvalid
		}
	}
	return nil
}
