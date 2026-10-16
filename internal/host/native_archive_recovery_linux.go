//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"golang.org/x/sys/unix"
)

type nativeArchiveRuntimeState struct {
	units           nativeArchiveUnitFacts
	enginePresent   bool
	frontendPresent bool
	backupPresent   bool
}

func (h archiveHost) nativeArchiveRecoveryRuntime(ctx context.Context, p archive.Plan) (nativeArchiveRuntimeState, error) {
	var zero nativeArchiveRuntimeState
	facts, err := h.nativeArchiveOwnedUnits(ctx, p.Candidate)
	d, _ := p.Candidate.Databases[0].Native()
	if err != nil {
		return zero, archive.ErrRefused
	}
	if err = h.nativeArchiveMaintenanceQuiescent(ctx, p, d); err != nil {
		return zero, err
	}
	if facts.frontend != nil {
		stopped, err := h.stoppedOrAbsentService(ctx, p.Candidate.App+".service")
		if err != nil || !stopped {
			return zero, archive.ErrRefused
		}
	}
	containers, err := listEngineContainers(ctx, h.session)
	if err != nil || !nativeRestoreContainersAbsent(containers, d.Binding.BindingID, p.Candidate.App) {
		return zero, archive.ErrRefused
	}
	if err = h.verifyNativeArchiveClosedRoute(ctx, p); err != nil {
		return zero, err
	}
	units, err := openManagedUnits(h.home)
	if err != nil {
		return zero, err
	}
	defer func() { _ = units.Close() }()
	name, hash := facts.engine.Name, facts.engine.Hash
	active, present, err := units.read(ctx, filepath.Join(managedServiceDirectory, name))
	if err != nil || present && "sha256:"+managedServiceHash(active) != hash {
		return zero, archive.ErrRefused
	}
	retained, retainedPresent, err := units.read(ctx, filepath.Join(managedServiceStaging, "retired-"+string(p.ArchiveID)+"-"+name))
	if err != nil || retainedPresent && "sha256:"+managedServiceHash(retained) != hash || present == retainedPresent {
		return zero, archive.ErrRefused
	}
	state := nativeArchiveRuntimeState{units: facts, enginePresent: present}
	if facts.frontend != nil {
		front := *facts.frontend
		state.frontendPresent, err = quadlet.InspectArchiveRemove(ctx, h.home, front.Name, front.Hash)
		if err != nil {
			return zero, err
		}
	}
	backups, err := h.nativeArchiveBackups(ctx, p)
	if err != nil {
		return zero, err
	}
	state.backupPresent, err = backups.inspect(ctx)
	return state, err
}

func (h archiveHost) inspectNativeArchiveRuntime(ctx context.Context, p archive.Plan) (archive.RuntimeInspection, error) {
	state, err := h.nativeArchiveRecoveryRuntime(ctx, p)
	if err != nil {
		return archive.RuntimeInspection{}, err
	}
	return archive.RuntimeInspection{Complete: !state.enginePresent && !state.frontendPresent && !state.backupPresent}, nil
}

func (h archiveHost) reconcileNativeArchiveRuntime(ctx context.Context, p archive.Plan) error {
	state, err := h.nativeArchiveRecoveryRuntime(ctx, p)
	if err != nil {
		return err
	}
	backups, err := h.nativeArchiveBackups(ctx, p)
	if err != nil {
		return err
	}
	if err = backups.retire(ctx); err != nil {
		return err
	}
	if state.frontendPresent {
		front := *state.units.frontend
		if h.units == nil || h.units.RemoveArchived(ctx, front.Name, front.Hash) != nil {
			return archive.ErrRecovery
		}
	}
	if state.enginePresent {
		if err = h.retainNativeArchiveUnit(ctx, p, state.units.engine); err != nil {
			return err
		}
	} else if err = h.syncNativeArchivedEngineUnit(ctx); err != nil {
		return err
	}
	result, err := h.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "daemon-reload"}, nil, true)
	if err != nil || result.ExitCode != 0 || result.Truncated {
		return archive.ErrRecovery
	}
	inspected, err := h.inspectNativeArchiveRuntime(ctx, p)
	if err != nil || !inspected.Complete {
		return archive.ErrRecovery
	}
	return nil
}

func (h archiveHost) syncNativeArchivedEngineUnit(ctx context.Context) error {
	units, err := openManagedUnits(h.home)
	if err != nil {
		return err
	}
	defer func() { _ = units.Close() }()
	if err = units.syncDirectory(managedServiceDirectory); err != nil {
		return err
	}
	if err = units.syncDirectory(managedServiceStaging); err != nil {
		return err
	}
	return ctx.Err()
}

func (h archiveHost) checkNativeArchiveRecovery(ctx context.Context, p archive.Plan, o archive.Operation, m archive.Manifest) error {
	pol, err := h.service.Policy.Load(ctx)
	if err != nil || pol.Hash() != p.PolicyHash {
		return archive.ErrRefused
	}
	current, exists, err := h.service.Store.ReadArchiveOperation(ctx, p.ID)
	if err != nil || !exists || !reflect.DeepEqual(current, o) || !m.Valid() || m.Plan.ID != p.ID || m.OperationID != o.ID {
		return archive.ErrRefused
	}
	d, _ := p.Candidate.Databases[0].Native()
	if _, err = h.service.Store.ReadNativeArchiveFence(ctx, p.ArchiveID, d.Binding.BindingID); err != nil {
		return err
	}
	if _, err = h.nativeArchiveMaintenanceAdmission(ctx, p, m, d, pol); err != nil {
		return err
	}
	snapshot, err := h.service.Inventory.Collect(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot.Identity)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if p.TargetHash != "sha256:"+hex.EncodeToString(sum[:]) || h.nativeArchiveMaintenanceQuiescent(ctx, p, d) != nil {
		return archive.ErrRefused
	}
	if err = h.verifyNativeArchiveDataSeal(ctx, p, d); err != nil {
		return err
	}
	if verified, ok := h.recoveryVerified[string(p.ArchiveID)]; ok && time.Since(verified) <= archive.MaxEvidenceAge {
		return nil
	}
	receipt, _, err := h.replayNativeArchivedSource(ctx, p, d)
	if err != nil || h.recoveryVerified == nil {
		return archive.ErrRefused
	}
	h.recoveryVerified[string(p.ArchiveID)] = receipt.VerifiedAt
	return nil
}

func (h archiveHost) verifyNativeArchiveDataSeal(ctx context.Context, p archive.Plan, d archive.NativeDatabase) error {
	point, err := h.service.Store.ReadNativePreparedPoint(ctx, nativeArchiveDatabasePointID(d))
	if err != nil || d.Allocation == nil {
		return archive.ErrRefused
	}
	base, err := engineOpenRoot(string(d.Binding.Root))
	if err != nil {
		return err
	}
	defer func() { _ = base.Close() }()
	source, present, err := sealNativeArchiveLocation(ctx, base, d.Binding.RelativeDirectory)
	if err != nil {
		return err
	}
	archived, retained, err := sealNativeArchiveLocation(ctx, base, filepath.Join("archives", string(p.ArchiveID), "databases", string(d.Binding.DatabaseID)))
	if err != nil || present == retained {
		return archive.ErrRefused
	}
	if !present {
		source = archived
	}
	if !engineSourceSealMatches(source, point.SourceSealSHA256) || source.root.Device != d.Allocation.DirectoryDevice || source.root.Inode != d.Allocation.DirectoryInode {
		return archive.ErrRefused
	}
	return nil
}

func sealNativeArchiveLocation(ctx context.Context, base *os.Root, relative string) (engineTreeSeal, bool, error) {
	file, err := engineOpenMember(base, relative, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if errors.Is(err, unix.ENOENT) {
		return engineTreeSeal{}, false, nil
	}
	if err != nil {
		return engineTreeSeal{}, false, err
	}
	defer func() { _ = file.Close() }()
	root, err := os.OpenRoot("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
	if err != nil {
		return engineTreeSeal{}, false, err
	}
	defer func() { _ = root.Close() }()
	seal, err := sealOwnedEngineSource(ctx, root, engineCopyLimits{Bytes: 64 << 20, Files: 16384, Depth: 16})
	return seal, err == nil, err
}
