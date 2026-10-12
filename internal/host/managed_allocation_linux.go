//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
	"golang.org/x/sys/unix"
)

const managedEmptyConfig = "[module-http]\nenabled = false\n"

// PrepareManaged allocates only a private, unpublished tree. It never launches
// native processes or a frontend, delivers keys, uploads Wasm, or loads ingress.
func (p DataPreparation) PrepareManaged(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) (err error) {
	if p.State == nil || p.Runner == nil || p.managedObserver == nil || planned.Managed == nil || desired.SpacetimeDB == nil || planned.MappingImage != data.MappingProbeImage || len(planned.PreparationRoots) != 1 {
		return data.ErrInvalid
	}
	m := planned.Managed
	if !m.Valid() || m.State != data.EngineUnpublished || m.Database.Root != desired.SpacetimeDB.PersistentRoot || m.Database.Name != desired.SpacetimeDB.Name || m.Archive.Destination != desired.BackupDestinations[0] {
		return data.ErrInvalid
	}
	if _, err = p.State.ReserveManagedPreparation(ctx, operation, planned, managedServiceHash([]byte(managedEmptyConfig))); err != nil {
		return err
	}
	if err = data.PrepareEngineLocks(ctx, p.StateRoot, m.Database.BindingID); err != nil {
		return err
	}
	if _, err = p.State.CommitManagedDesired(ctx, operation, 0, managedDesiredRecord(operation, planned, 1, plan.ManagedLifecyclePlan{})); err != nil {
		return err
	}
	fence, err := p.State.HoldDataFence(ctx, m.Database.DatabaseID, operation)
	if err != nil {
		return err
	}
	preparation, err := p.State.AcquireEnginePreparation(ctx, m.Database.BindingID, fence.ID, operation)
	if err != nil {
		return err
	}
	defer func() {
		if preparation != nil {
			err = errors.Join(err, preparation.Close())
		}
	}()
	if err = p.managedObserver.sourceAbsent(ctx, m.Database); err != nil {
		return err
	}
	pull, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err = p.Runner.Run(pull, "podman", "pull", data.MappingProbeImage)
	cancel()
	if err != nil {
		return err
	}
	probeRoot := p.ProbeRoot
	if probeRoot == nil {
		probeRoot = data.ProbeRoot
	}
	root, err := probeRoot(ctx, string(m.Database.Root))
	expected := planned.PreparationRoots[0]
	if err != nil || !root.Admits(m.Database.Root, desired.MinimumFreeDiskBytes) || root.Device != expected.Device || root.Inode != expected.Inode || root.Filesystem != expected.Filesystem {
		return errors.Join(data.ErrInvalid, err)
	}
	probeMapping := p.ProbeMapping
	if probeMapping == nil {
		probeMapping = inventory.ProbeDataMapping
	}
	mapping, err := probeMapping(ctx, p.Runner, root, desired.SpacetimeDB.Runtime)
	if err != nil || mapping.Image != data.MappingProbeImage || !mapping.Admits(desired.SpacetimeDB.Runtime, root) {
		return errors.Join(data.ErrInvalid, err)
	}
	if _, err = data.PrepareSpacetimeDirectory(m.Database); err != nil {
		return err
	}
	receipt, err := data.CaptureSpacetimeAllocation(m.Database)
	if err != nil {
		return err
	}
	receipt.RootProof = &root
	receipt.MappingProof = &mapping
	if !receipt.Valid() {
		return data.ErrInvalid
	}
	// Store allocation commits acquire the binding guard themselves. Release it
	// before that commit, then reacquire and recheck the owned empty inode.
	if err = preparation.Close(); err != nil {
		return err
	}
	preparation = nil
	// Persist the empty allocation identity before writing the fixed disabled config.
	if err = p.State.SaveEngineAllocation(ctx, m.Database.BindingID, receipt); err != nil {
		return err
	}
	preparation, err = p.State.AcquireEnginePreparation(ctx, m.Database.BindingID, fence.ID, operation)
	if err != nil {
		return err
	}
	if err = p.managedObserver.sourceAbsent(ctx, m.Database); err != nil {
		return err
	}
	fresh, err := data.CaptureSpacetimeAllocation(m.Database)
	if err != nil || fresh.DirectoryDevice != receipt.DirectoryDevice || fresh.DirectoryInode != receipt.DirectoryInode || fresh.RootDevice != receipt.RootDevice || fresh.RootInode != receipt.RootInode {
		return errors.Join(data.ErrInvalid, err)
	}
	return writeManagedEmptyConfig(ctx, m.Database)
}

func writeManagedEmptyConfig(ctx context.Context, binding data.SpacetimeDBBinding) error {
	if binding.Validate() != nil || ctx.Err() != nil {
		return data.ErrInvalid
	}
	root, err := engineOpenRoot(binding.SourcePath())
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	file, err := engineOpenMember(root, "config.toml", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write([]byte(managedEmptyConfig))
	err = errors.Join(writeErr, file.Sync(), file.Close(), ctx.Err())
	if err != nil {
		return err
	}
	directory, err := engineOpenMember(root, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (p DataPreparation) ManagedPrepared(ctx context.Context, operation string, planned plan.Plan, desired policy.Desired) (bool, error) {
	if p.State == nil || p.managedObserver == nil || planned.Managed == nil || desired.SpacetimeDB == nil {
		return false, data.ErrInvalid
	}
	m := planned.Managed
	wanted, err := p.State.ReadManagedDesired(ctx, m.Database.BindingID)
	if err != nil || wanted != managedDesiredRecord(operation, planned, 1, plan.ManagedLifecyclePlan{}) {
		return false, errors.Join(data.ErrInvalid, err)
	}
	recorded, err := p.State.ReadManagedPreparation(ctx, m.Database.BindingID)
	if err != nil {
		return false, err
	}
	if recorded.PlanID != planned.Hash || recorded.OperationID != operation || recorded.ConfigSHA256 != managedServiceHash([]byte(managedEmptyConfig)) || target.Port(recorded.EnginePort) != m.EnginePort {
		return false, data.ErrInvalid
	}
	frontend := target.Port(0)
	if m.Frontend != nil {
		frontend = m.Frontend.Port
	}
	if target.Port(recorded.FrontendPort) != frontend {
		return false, data.ErrInvalid
	}
	if err := p.managedObserver.sourceAbsent(ctx, m.Database); err != nil {
		return false, err
	}
	projection, err := p.State.ReadManagedApp(ctx, planned.App)
	if err != nil || projection.Reserved.Database != m.Database || projection.Reserved.Archive != m.Archive || projection.Permit != nil {
		return false, errors.Join(data.ErrInvalid, err)
	}
	receipt, err := p.State.ReadAllocation(ctx, m.Database.DatabaseID)
	if err != nil || receipt == nil || !receipt.Valid() || receipt.RootProof == nil || receipt.MappingProof == nil {
		return false, errors.Join(data.ErrInvalid, err)
	}
	currentRoot, err := data.InspectRoot(string(m.Database.Root))
	if err != nil || currentRoot.Device != receipt.RootDevice || currentRoot.Inode != receipt.RootInode {
		return false, errors.Join(data.ErrInvalid, err)
	}
	root, err := engineOpenRoot(m.Database.SourcePath())
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	directory, err := engineOpenMember(root, ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return false, err
	}
	var identity unix.Stat_t
	statErr := unix.Fstat(int(directory.Fd()), &identity)
	entries, readErr := directory.ReadDir(2)
	closeErr := directory.Close()
	if statErr != nil || closeErr != nil || uint64(identity.Dev) != receipt.DirectoryDevice || identity.Ino != receipt.DirectoryInode || identity.Mode&07777 != 0700 || int(identity.Uid) != os.Geteuid() {
		return false, errors.Join(data.ErrInvalid, statErr, closeErr)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) != 1 || entries[0].Name() != "config.toml" {
		return false, errors.Join(data.ErrInvalid, readErr)
	}
	raw, err := readEnginePrivateFile(ctx, root, "config.toml", recorded.ConfigSHA256, 4096)
	if err != nil || !bytes.Equal(raw, []byte(managedEmptyConfig)) {
		return false, errors.Join(data.ErrInvalid, err)
	}
	return true, ctx.Err()
}
