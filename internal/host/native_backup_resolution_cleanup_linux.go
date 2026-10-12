//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"golang.org/x/sys/unix"
)

func (o nativeBackupResolutionOperations) cleanupOriginalUpload(ctx context.Context, v *nativeBackupObserved) error {
	if err := o.disposeOriginalUploadReaders(ctx, v.retained.Workspace); err != nil {
		return err
	}
	if err := nativeUploadProcessesAbsent(ctx, v.retained.Workspace); err != nil {
		return err
	}
	if v.originalParent == nil {
		if _, err := os.Lstat(v.retained.Workspace); !os.IsNotExist(err) {
			return errEngineSource
		}
		return nil
	}
	err := discardNativeWorkspace(v.originalParent, v.retained.Workspace)
	if err == nil {
		v.originalParent = nil
	}
	return err
}

func (o nativeBackupResolutionOperations) disposeOriginalUploadReaders(ctx context.Context, workspace string) error {
	containers, err := listEngineContainers(ctx, o.runtime.session)
	if err != nil {
		return err
	}
	for _, container := range containers {
		if !nativeUploadContainerUses(container, workspace) {
			continue
		}
		if err = o.disposeOriginalUploadReader(ctx, container, workspace); err != nil {
			return err
		}
	}
	remaining, err := listEngineContainers(ctx, o.runtime.session)
	if err != nil {
		return err
	}
	for _, container := range remaining {
		if nativeUploadContainerUses(container, workspace) {
			return errEngineSource
		}
	}
	return nil
}
func nativeUploadContainerUses(container engineContainer, workspace string) bool {
	for _, mount := range container.Mounts {
		if nativeUploadContains(workspace, mount.Source) {
			return true
		}
	}
	return false
}
func (o nativeBackupResolutionOperations) disposeOriginalUploadReader(ctx context.Context, container engineContainer, workspace string) error {
	nonce := container.Config.Labels["BRINE_PRIVATE_VERIFY"]
	if !data.ValidID(nonce) || container.Name != "brine-native-read-"+nonce || container.State.Running == nil || *container.State.Running || container.State.PID == nil || *container.State.PID != 0 {
		return errEngineSource
	}
	pin, err := readNativeReaderPin(ctx)
	if err != nil {
		return err
	}
	var source string
	for _, mount := range container.Mounts {
		if nativeUploadContains(workspace, mount.Source) && mount.Destination == "/copy/replica" {
			if source != "" {
				return errEngineSource
			}
			source = mount.Source
		}
	}
	if source == "" {
		return errEngineSource
	}
	// This fixed metadata checker verifies the original image, command, security,
	// owner label and exact read-copy mount. It does not manufacture reader proof.
	view := &nativeReadView{nonce: nonce, path: filepath.Dir(source), replica: filepath.Base(source)}
	if !stoppedNativeReader(container, pin, view) {
		return errEngineSource
	}
	_, err = o.runtime.session.Execute(ctx, "/usr/bin/podman", []string{"container", "rm", container.ID}, nil, true)
	return err
}
func nativeUploadContains(workspace, path string) bool {
	clean := strings.TrimSuffix(path, " (deleted)")
	if !filepath.IsAbs(clean) {
		return false
	}
	return clean == workspace || strings.HasPrefix(clean, workspace+string(filepath.Separator))
}

func nativeUploadProcessesAbsent(ctx context.Context, workspace string) error {
	proc, err := os.Open("/proc")
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = proc.Close() }()
	entries, err := proc.ReadDir(4097)
	if err != nil && !errors.Is(err, os.ErrNotExist) && len(entries) == 0 {
		return errEngineSource
	}
	if len(entries) > 4096 {
		return errEngineSource
	}
	for _, entry := range entries {
		pid, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || pid == 0 || pid == uint64(os.Getpid()) {
			continue
		}
		if err = nativeUploadProcessAbsent(ctx, filepath.Join("/proc", strconv.FormatUint(pid, 10)), workspace); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func nativeUploadProcessAbsent(ctx context.Context, process, workspace string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, process, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS, &stat); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return errEngineSource
	}
	if uint64(stat.Uid) != uint64(os.Geteuid()) {
		return nil
	}
	cwd, err := os.Readlink(filepath.Join(process, "cwd"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errEngineSource
	}
	if nativeUploadContains(workspace, cwd) {
		return errEngineSource
	}
	fd, err := os.Open(filepath.Join(process, "fd"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errEngineSource
	}
	defer func() { _ = fd.Close() }()
	entries, err := fd.ReadDir(4097)
	if err != nil && len(entries) == 0 {
		return errEngineSource
	}
	if len(entries) > 4096 {
		return errEngineSource
	}
	for _, entry := range entries {
		number, parseErr := strconv.ParseUint(entry.Name(), 10, 32)
		if parseErr != nil {
			return errEngineSource
		}
		target, readErr := os.Readlink(filepath.Join(process, "fd", strconv.FormatUint(number, 10)))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return errEngineSource
		}
		if nativeUploadContains(workspace, target) {
			return errEngineSource
		}
	}
	return nil
}

func (o nativeBackupResolutionOperations) cleanupInspectionWorkspaces(ctx context.Context, i *store.NativeBackupInspection, permit store.EnginePermit) error {
	workspaces, err := i.Workspaces(ctx)
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		if workspace.EpochID != permit.Runtime.EpochID {
			return errEngineSource
		}
		path := filepath.Join(string(permit.Reserved.Database.Root), workspace.Relative)
		if err = o.disposeOriginalUploadReaders(ctx, path); err != nil {
			return err
		}
		if err = nativeUploadProcessesAbsent(ctx, path); err != nil {
			return err
		}
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			if err = i.DisposeWorkspace(ctx, workspace.ID); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return errEngineSource
		}
		parent, err := engineOpenRoot(path)
		if err != nil {
			return err
		}
		identity, err := nativeRestoreDirectoryIdentity(parent, ".")
		if err != nil || identity.Device != workspace.Directory.Device || identity.Inode != workspace.Directory.Inode {
			_ = parent.Close()
			return errEngineSource
		}
		err = discardNativeWorkspace(parent, path)
		if err != nil {
			_ = parent.Close()
			return err
		}
		if err = i.DisposeWorkspace(ctx, workspace.ID); err != nil {
			return err
		}
	}
	return nil
}
